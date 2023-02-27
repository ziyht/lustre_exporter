// (C) Copyright 2017 Hewlett Packard Enterprise Development LP
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"

	"lustre_exporter/log"
	"lustre_exporter/sources"
)

type labelPair struct {
	Name  string
	Value string
}

type promType struct {
	Name   string
	Help   string
	Type   int
	Labels []labelPair
	Value  float64
	Parsed bool // Parsed returns 'true' if the metric has already been parsed
}

func (p *promType)String()string{
  // {"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "connect"}, {"target", "lustrefs-OST0000"}}, 1, false},
	return  fmt.Sprintf(`{"%s", "%s", %s, []labelPair{%s}, %v, %v} `, p.Name, p.Help, p.typeStr(), p.lablesStr(), p.Value, p.Parsed)
}

func (p *promType)typeStr() string {
	switch p.Type{
		case 0: return "counter"
		case 1: return "gauge"
		case 3: return "untyped"
	}

	return "(invalid type)"
}

func (p *promType)lablesStr() string {
	return "(todo)"
}

func (p *promType)isSameMetric(in *promType) bool {
	if p.Name != in.Name { return false }
	if p.Help != in.Help { return false }
	if !reflect.DeepEqual(p.Labels, in.Labels) { return false }
	if p.Type != in.Type { return false }

	return true
}

const (
	// Constants taken from https://github.com/prometheus/client_model/blob/master/go/metrics.pb.go
	counter = 0
	gauge   = 1
	untyped = 3
)

var (
	errMetricNotFound      = errors.New("metric not found")
	errMetricAlreadyParsed = errors.New("metric already parsed")
	errMetricValueNotEqual = errors.New("metric value not equal")
)

func toggleCollectors(target string) {
	switch target {
	case "OST":
		sources.OstEnabled = "extended"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "disabled"
	case "MDT":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "extended"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "disabled"
	case "MGS":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "extended"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "disabled"
	case "MDS":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "extended"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "disabled"
	case "Client":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "extended"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "disabled"
	case "Generic":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "extended"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "disabled"
	case "LNET":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "extended"
		sources.HealthStatusEnabled = "disabled"
	case "Health":
		sources.OstEnabled = "disabled"
		sources.MdtEnabled = "disabled"
		sources.MgsEnabled = "disabled"
		sources.MdsEnabled = "disabled"
		sources.ClientEnabled = "disabled"
		sources.GenericEnabled = "disabled"
		sources.LnetEnabled = "disabled"
		sources.HealthStatusEnabled = "extended"
	}
}

func stringAlphabetize(str1 string, str2 string) (int, error) {
	var letterCount int
	if len(str1) > len(str2) {
		letterCount = len(str2)
	} else {
		letterCount = len(str1)
	}

	for i := 0; i < letterCount-1; i++ {
		if str1[i] == str2[i] {
			continue
		} else if str1[i] > str2[i] {
			return 1, nil
		} else {
			return 2, nil
		}
	}

	return 0, fmt.Errorf("Duplicate label detected: %q", str1)
}

func sortByKey(labels []labelPair) ([]labelPair, error) {
	if len(labels) < 2 {
		return labels, nil
	}
	labelUpdate := make([]labelPair, len(labels))

	for i := range labels {
		desiredIndex := 0
		for j := range labels {
			if i == j {
				continue
			}

			result, err := stringAlphabetize(labels[i].Name, labels[j].Name)
			if err != nil {
				return nil, err
			}

			if result == 1 {
				//label for i's Name comes after label j's Name
				desiredIndex++
			}
		}
		labelUpdate[desiredIndex] = labels[i]
	}
	return labelUpdate, nil
}

func compareResults(parsedMetric promType, expectedMetrics []promType) ([]promType, error) {
	for i := range expectedMetrics {
		if parsedMetric.isSameMetric(&expectedMetrics[i]){
			if expectedMetrics[i].Parsed {
				return expectedMetrics, errMetricAlreadyParsed
			}

			if parsedMetric.Value != expectedMetrics[i].Value {
				return expectedMetrics, errMetricValueNotEqual
			}

			expectedMetrics[i].Parsed = true
			return expectedMetrics, nil
		}
	}
	return expectedMetrics, errMetricNotFound
}

func compareResults_(parsedMetric promType, expectedMetrics []promType) ([]promType, error) {
	for i := range expectedMetrics {
		if reflect.DeepEqual(parsedMetric, expectedMetrics[i]) {
			if expectedMetrics[i].Parsed {
				return expectedMetrics, errMetricAlreadyParsed
			}
			expectedMetrics[i].Parsed = true
			return expectedMetrics, nil
		}
	}
	return expectedMetrics, errMetricNotFound
}

func blacklisted(blacklist []string, metricName string) bool {
	for _, name := range blacklist {
		if strings.HasPrefix(metricName, name) {
			return true
		}
	}
	return false
}

var	expectedMetrics = []promType{
		// OST Metrics
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "connect"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "connect"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "connect"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "connect"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 2, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "create"}, {"target", "lustrefs-OST0002"}}, 2, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "create"}, {"target", "lustrefs-OST0004"}}, 2, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "create"}, {"target", "lustrefs-OST0006"}}, 2, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "ping"}, {"target", "lustrefs-OST0000"}}, 141, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "ping"}, {"target", "lustrefs-OST0002"}}, 645, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "ping"}, {"target", "lustrefs-OST0004"}}, 645, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "ping"}, {"target", "lustrefs-OST0006"}}, 645, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 35359, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "statfs"}, {"target", "lustrefs-OST0002"}}, 35354, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "statfs"}, {"target", "lustrefs-OST0004"}}, 35350, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"operation", "statfs"}, {"target", "lustrefs-OST0006"}}, 35347, false},
		{"lustre_lfsck_speed_limit", "Maximum operations per second LFSCK (Lustre filesystem verification) can run", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_lfsck_speed_limit", "Maximum operations per second LFSCK (Lustre filesystem verification) can run", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_lfsck_speed_limit", "Maximum operations per second LFSCK (Lustre filesystem verification) can run", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_recovery_time_soft_seconds", "Duration in seconds for a client to attempt to reconnect after a crash (automatically incremented if servers are still in an error state)", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 150, false},
		{"lustre_recovery_time_soft_seconds", "Duration in seconds for a client to attempt to reconnect after a crash (automatically incremented if servers are still in an error state)", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 150, false},
		{"lustre_recovery_time_soft_seconds", "Duration in seconds for a client to attempt to reconnect after a crash (automatically incremented if servers are still in an error state)", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 150, false},
		{"lustre_recovery_time_soft_seconds", "Duration in seconds for a client to attempt to reconnect after a crash (automatically incremented if servers are still in an error state)", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 150, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 46956, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 64575, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 71996, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 56048, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 60729, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 64208, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 57773, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 30804, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 28787, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 32411, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 29079, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 27390, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 29417, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 29390, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 28191, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 25852, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 26061, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 55053, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 27161, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 27210, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 7906, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 8254, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 7810, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 8579, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 8051, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 8176, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 7404, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 8246, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 7887, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 7969, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 7632, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 7786, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 7609, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 7656, false},
		{"lustre_job_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 7722, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.5927284e+07, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 1.474012704e+09, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 9.82675104e+08, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 9.82675104e+08, false},
		{"lustre_recovery_time_hard_seconds", "Maximum timeout 'recover_time_soft' can increment to for a single server", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 900, false},
		{"lustre_recovery_time_hard_seconds", "Maximum timeout 'recover_time_soft' can increment to for a single server", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 900, false},
		{"lustre_recovery_time_hard_seconds", "Maximum timeout 'recover_time_soft' can increment to for a single server", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 900, false},
		{"lustre_exports_total", "Total number of times the pool has been exported", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 3, false},
		{"lustre_exports_total", "Total number of times the pool has been exported", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 3, false},
		{"lustre_exports_total", "Total number of times the pool has been exported", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 3, false},
		{"lustre_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.298711e+06, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 1.6552048697344e+13, false},
		{"lustre_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.7029274624e+10, false},
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 4.7168396288e+10, false},
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 3.1445593088e+10, false},
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 3.1445593088e+10, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 23, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0002"}}, 23, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0004"}}, 23, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0006"}}, 23, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 4.09674e+06, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "10"}, {"target", "lustrefs-OST0000"}}, 3, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0000"}}, 174382, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "3"}, {"target", "lustrefs-OST0000"}}, 20244, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4"}, {"target", "lustrefs-OST0000"}}, 4037, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "5"}, {"target", "lustrefs-OST0000"}}, 1577, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "6"}, {"target", "lustrefs-OST0000"}}, 925, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "7"}, {"target", "lustrefs-OST0000"}}, 579, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 190, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "9"}, {"target", "lustrefs-OST0000"}}, 35, false},
		{"lustre_job_cleanup_interval_seconds", "Interval in seconds between cleanup of tuning statistics", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 600, false},
		{"lustre_job_cleanup_interval_seconds", "Interval in seconds between cleanup of tuning statistics", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 600, false},
		{"lustre_job_cleanup_interval_seconds", "Interval in seconds between cleanup of tuning statistics", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 600, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 13, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0002"}}, 13, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0004"}}, 13, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0006"}}, 13, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0000"}}, 10, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0002"}}, 10, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0004"}}, 10, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0006"}}, 10, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 153, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0000"}}, 4.059303e+06, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0000"}}, 13817, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0000"}}, 1367, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0000"}}, 157, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0000"}}, 58945, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0000"}}, 2911, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4"}, {"target", "lustrefs-OST0000"}}, 358, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0000"}}, 154861, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0000"}}, 6161, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 679, false},
		{"lustre_grant_precreate_capacity_bytes", "Maximum space in bytes that clients can preallocate for objects", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.0828928e+07, false},
		{"lustre_grant_precreate_capacity_bytes", "Maximum space in bytes that clients can preallocate for objects", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 4.0828928e+07, false},
		{"lustre_grant_precreate_capacity_bytes", "Maximum space in bytes that clients can preallocate for objects", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 4.0828928e+07, false},
		{"lustre_grant_precreate_capacity_bytes", "Maximum space in bytes that clients can preallocate for objects", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 4.0828928e+07, false},
		{"lustre_soft_sync_limit", "Number of RPCs necessary before triggering a sync", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 16, false},
		{"lustre_soft_sync_limit", "Number of RPCs necessary before triggering a sync", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 16, false},
		{"lustre_soft_sync_limit", "Number of RPCs necessary before triggering a sync", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 16, false},
		{"lustre_degraded", "Binary indicator as to whether or not the pool is degraded - 0 for not degraded, 1 for degraded", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_degraded", "Binary indicator as to whether or not the pool is degraded - 0 for not degraded, 1 for degraded", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_degraded", "Binary indicator as to whether or not the pool is degraded - 0 for not degraded, 1 for degraded", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 1.048576e+06, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 1.048576e+06, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 1.048576e+06, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 1.55500642304e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 2.15147593728e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 2.40823762944e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 1.85838792704e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 2.01320374272e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 2.13963751424e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 1.90641672192e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 1.01291593728e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 9.6291004416e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 1.08391518208e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 9.6846708736e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 9.095131136e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 9.837008896e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 9.6616685568e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 9.4293942272e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 8.5505073152e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 8.6011281408e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 1.82426591232e+11, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 8.9955606528e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 9.0020810752e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 2.1711745024e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 2.370156544e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 2.1836828672e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 2.4320741376e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 2.3049785344e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 2.331467776e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 1.9928662016e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 2.3387901952e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 2.196652032e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 2.1969199104e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 2.0787339264e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 2.1327118336e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 2.080346112e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 2.1190914048e+10, false},
		{"lustre_job_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 2.0557369344e+10, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_job_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.7168367616e+10, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 4.71684096e+10, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 3.14456064e+10, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 3.14456064e+10, false},
		{"lustre_exports_granted_total", "Total number of exports that have been marked granted", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 5.94706432e+09, false},
		{"lustre_exports_granted_total", "Total number of exports that have been marked granted", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 5.0528256e+07, false},
		{"lustre_exports_granted_total", "Total number of exports that have been marked granted", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 5.0528256e+07, false},
		{"lustre_exports_granted_total", "Total number of exports that have been marked granted", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 5.0528256e+07, false},
		{"lustre_grant_compat_disabled", "Binary indicator as to whether clients with OBD_CONNECT_GRANT_PARAM setting will be granted space", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_grant_compat_disabled", "Binary indicator as to whether clients with OBD_CONNECT_GRANT_PARAM setting will be granted space", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_grant_compat_disabled", "Binary indicator as to whether clients with OBD_CONNECT_GRANT_PARAM setting will be granted space", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_sync_journal_enabled", "Binary indicator as to whether or not the journal is set for asynchronous commits", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_sync_journal_enabled", "Binary indicator as to whether or not the journal is set for asynchronous commits", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_sync_journal_enabled", "Binary indicator as to whether or not the journal is set for asynchronous commits", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0000"}}, 2, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0002"}}, 2, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0004"}}, 2, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0006"}}, 2, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1048576"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2048"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2048"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2048"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2097152"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4096"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4096"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4096"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4194304"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 4, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0002"}}, 4, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0004"}}, 4, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0006"}}, 4, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8192"}, {"target", "lustrefs-OST0000"}}, 12, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8192"}, {"target", "lustrefs-OST0002"}}, 12, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8192"}, {"target", "lustrefs-OST0004"}}, 12, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8192"}, {"target", "lustrefs-OST0006"}}, 12, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1048576"}, {"target", "lustrefs-OST0000"}}, 58945, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "131072"}, {"target", "lustrefs-OST0000"}}, 2911, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16384"}, {"target", "lustrefs-OST0000"}}, 358, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2048"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2048"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2048"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2097152"}, {"target", "lustrefs-OST0000"}}, 154861, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "262144"}, {"target", "lustrefs-OST0000"}}, 6161, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "32768"}, {"target", "lustrefs-OST0000"}}, 679, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4096"}, {"target", "lustrefs-OST0000"}}, 153, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4096"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4096"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4096"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4194304"}, {"target", "lustrefs-OST0000"}}, 4.059303e+06, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "524288"}, {"target", "lustrefs-OST0000"}}, 13817, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "65536"}, {"target", "lustrefs-OST0000"}}, 1367, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8192"}, {"target", "lustrefs-OST0000"}}, 157, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8192"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8192"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8192"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_brw_size_megabytes", "Block read/write size in megabytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4, false},
		{"lustre_brw_size_megabytes", "Block read/write size in megabytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 4, false},
		{"lustre_brw_size_megabytes", "Block read/write size in megabytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 4, false},
		{"lustre_brw_size_megabytes", "Block read/write size in megabytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 4, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.5927028e+07, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 1.474012448e+09, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 9.82674848e+08, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 9.82674848e+08, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "create"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "destroy"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "get_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "getattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "punch"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "quotactl"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "set_info"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "setattr"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "statfs"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"operation", "sync"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0000"}}, 23, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0002"}}, 23, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0004"}}, 23, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0006"}}, 23, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0000"}}, 153, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 157, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "10"}, {"target", "lustrefs-OST0000"}}, 176, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "11"}, {"target", "lustrefs-OST0000"}}, 164, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "12"}, {"target", "lustrefs-OST0000"}}, 187, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "13"}, {"target", "lustrefs-OST0000"}}, 185, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "14"}, {"target", "lustrefs-OST0000"}}, 168, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "15"}, {"target", "lustrefs-OST0000"}}, 168, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0000"}}, 186, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "17"}, {"target", "lustrefs-OST0000"}}, 181, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "18"}, {"target", "lustrefs-OST0000"}}, 170, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "19"}, {"target", "lustrefs-OST0000"}}, 164, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0000"}}, 158, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "20"}, {"target", "lustrefs-OST0000"}}, 187, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "21"}, {"target", "lustrefs-OST0000"}}, 174, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "22"}, {"target", "lustrefs-OST0000"}}, 168, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "23"}, {"target", "lustrefs-OST0000"}}, 178, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "24"}, {"target", "lustrefs-OST0000"}}, 179, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "25"}, {"target", "lustrefs-OST0000"}}, 205, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "26"}, {"target", "lustrefs-OST0000"}}, 192, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "27"}, {"target", "lustrefs-OST0000"}}, 160, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "28"}, {"target", "lustrefs-OST0000"}}, 192, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "29"}, {"target", "lustrefs-OST0000"}}, 192, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "3"}, {"target", "lustrefs-OST0000"}}, 200, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "30"}, {"target", "lustrefs-OST0000"}}, 195, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "31"}, {"target", "lustrefs-OST0000"}}, 4.293275e+06, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "4"}, {"target", "lustrefs-OST0000"}}, 156, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "5"}, {"target", "lustrefs-OST0000"}}, 175, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "6"}, {"target", "lustrefs-OST0000"}}, 163, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "7"}, {"target", "lustrefs-OST0000"}}, 185, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 159, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "9"}, {"target", "lustrefs-OST0000"}}, 160, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 1, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0002"}}, 1, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0004"}}, 1, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0006"}}, 1, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_io_time_milliseconds_total", "Total time in milliseconds the filesystem has spent processing various object sizes.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_exports_dirty_total", "Total number of exports that have been marked dirty", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 5.3215232e+07, false},
		{"lustre_exports_dirty_total", "Total number of exports that have been marked dirty", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_exports_dirty_total", "Total number of exports that have been marked dirty", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_exports_dirty_total", "Total number of exports that have been marked dirty", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_exports_pending_total", "Total number of exports that have been marked pending", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 5.815533568e+09, false},
		{"lustre_exports_pending_total", "Total number of exports that have been marked pending", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 0, false},
		{"lustre_exports_pending_total", "Total number of exports that have been marked pending", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 0, false},
		{"lustre_exports_pending_total", "Total number of exports that have been marked pending", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 0, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4.7029276672e+10, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 4.7168398336e+10, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 3.1445595136e+10, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 3.1445595136e+10, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "23"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "24"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "25"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "26"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "27"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "28"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "29"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "30"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "31"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "32"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "33"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "34"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "35"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "36"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "37"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "38"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "39"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "40"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "41"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "42"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "43"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "44"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "45"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "46"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "47"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "48"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "49"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "50"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "51"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "52"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "53"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "54"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "55"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "56"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_job_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"jobid", "57"}, {"target", "lustrefs-OST0000"}}, 4.194304e+06, false},
		{"lustre_precreate_batch", "Maximum number of objects that can be included in a single transaction", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0002"}}, 128, false},
		{"lustre_precreate_batch", "Maximum number of objects that can be included in a single transaction", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0004"}}, 128, false},
		{"lustre_precreate_batch", "Maximum number of objects that can be included in a single transaction", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0006"}}, 128, false},
		{"lustre_job_cleanup_interval_seconds", "Interval in seconds between cleanup of tuning statistics", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 600, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "131072"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16384"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2048"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "262144"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "32768"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4096"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "524288"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "65536"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io_total", "Total number of operations the filesystem has performed for the given size.", counter, []labelPair{{"component", "ost"}, {"operation", "write"}, {"size", "2048"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "10"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "11"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "12"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "13"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "14"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "15"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "17"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "18"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "19"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "20"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "21"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "22"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "23"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "24"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "25"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "26"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "27"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "28"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "29"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "3"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "30"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "31"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "5"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "6"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "7"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_discontiguous_pages_total", "Total number of logical discontinuities per RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "9"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_degraded", "Binary indicator as to whether or not the pool is degraded - 0 for not degraded, 1 for degraded", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_exports_total", "Total number of times the pool has been exported", counter, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 3, false},
		{"lustre_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 4096, false},
		{"lustre_grant_compat_disabled", "Binary indicator as to whether clients with OBD_CONNECT_GRANT_PARAM setting will be granted space", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_recovery_time_hard_seconds", "Maximum timeout 'recover_time_soft' can increment to for a single server", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 900, false},
		{"lustre_lfsck_speed_limit", "Maximum operations per second LFSCK (Lustre filesystem verification) can run", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_pages_per_bulk_rw_total", "Total number of pages per block RPC.", counter, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_soft_sync_limit", "Number of RPCs necessary before triggering a sync", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 16, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "10"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "3"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "5"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "6"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "7"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_disk_io", "Current number of I/O operations that are processing during the snapshot.", gauge, []labelPair{{"component", "ost"}, {"operation", "read"}, {"size", "9"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_precreate_batch", "Maximum number of objects that can be included in a single transaction", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 128, false},
		{"lustre_sync_journal_enabled", "Binary indicator as to whether or not the journal is set for asynchronous commits", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 0, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "ost"}, {"target", "lustrefs-OST0000"}}, 1.048576e+06, false},

		// MDT Metrics
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "43"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "44"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "45"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "46"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "47"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "48"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "49"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "50"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "51"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "52"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "53"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "54"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "55"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "56"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "crossdir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "link"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "mkdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "mknod"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "rmdir"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "samedir_rename"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "setxattr"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "sync"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_job_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"jobid", "57"}, {"operation", "unlink"}, {"target", "lustrefs-MDT0000"}}, 0, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"operation", "close"}, {"target", "lustrefs-MDT0000"}}, 9, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"operation", "getattr"}, {"target", "lustrefs-MDT0000"}}, 16, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"operation", "open"}, {"target", "lustrefs-MDT0000"}}, 10, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"operation", "setattr"}, {"target", "lustrefs-MDT0000"}}, 57, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"operation", "statfs"}, {"target", "lustrefs-MDT0000"}}, 1, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "mdt"}, {"operation", "getxattr"}, {"target", "lustrefs-MDT0000"}}, 2, false},
		{"lustre_exports_total", "Total number of times the pool has been exported", counter, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 10, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 131072, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 2.24150656e+09, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 4.30405522e+08, false},
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 2.241498368e+09, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 4.30405292e+08, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "mdt"}, {"target", "lustrefs-MDT0000"}}, 2.241500416e+09, false},

		// MGS Metrics
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"target", "osd"}, {"component", "mgs"}}, 1.12074688e+09, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "mgs"}, {"target", "osd"}}, 131072, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "mgs"}, {"target", "osd"}}, 1.12075328e+09, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "mgs"}, {"target", "osd"}}, 2.31003975e+08, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "mgs"}, {"target", "osd"}}, 2.31004127e+08, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "mgs"}, {"target", "osd"}}, 1.120748928e+09, false},

		// Client Metrics
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0001-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0002-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0003-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0004-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0005-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0006-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 1537, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0001-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0002-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0003-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0004-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0005-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0006-osc-ffff88105db50000"}}, 0, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 1.638901e+07, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 117183, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 11916, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 1489, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 458648, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 24502, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "4"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 2876, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 1.145365e+06, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 51901, false},
		{"lustre_pages_per_rpc_total", "Total number of pages per RPC.", counter, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 5860, false},
		{"lustre_inodes_maximum", "The maximum number of inodes (objects) the filesystem can hold", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 4.30405497e+08, false},
		{"lustre_xattr_cache_enabled", "Returns '1' if extended attribute cache is enabled", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1, false},
		{"lustre_read_bytes_total", "The total number of bytes that have been read.", counter, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 4.194304e+06, false},
		{"lustre_write_samples_total", "Total number of writes that have been recorded.", counter, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 8.946781e+07, false},
		{"lustre_available_kilobytes", "Number of kilobytes readily available in the pool", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 2.8300029952e+11, false},
		{"lustre_maximum_read_ahead_megabytes", "Maximum number of megabytes to read ahead", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 64, false},
		{"lustre_statahead_agl_enabled", "Returns '1' if the Asynchronous Glimpse Lock (AGL) for statahead is enabled", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1, false},
		{"lustre_checksum_pages_enabled", "Returns '1' if data checksumming is enabled for the client", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1, false},
		{"lustre_read_maximum_size_bytes", "The maximum read size in bytes.", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 4.194304e+06, false},
		{"lustre_write_minimum_size_bytes", "The minimum write size in bytes.", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 4096, false},
		{"lustre_read_minimum_size_bytes", "The minimum read size in bytes.", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 4.194304e+06, false},
		{"lustre_lazystatfs_enabled", "Returns '1' if lazystatfs (a non-blocking alternative to statfs) is enabled for the client", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1, false},
		{"lustre_read_samples_total", "Total number of reads that have been recorded.", counter, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1, false},
		{"lustre_blocksize_bytes", "Filesystem block size in bytes", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1.048576e+06, false},
		{"lustre_maximum_ea_size_bytes", "Maximum Extended Attribute (EA) size in bytes", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 216, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0001-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0002-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0003-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0004-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0005-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0006-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1024"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1048576"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "128"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "131072"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "134217728"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "16"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "16384"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "16777216"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "2048"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "2097152"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "256"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "262144"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "32"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "32768"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "33554432"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "4096"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "4194304"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "512"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "524288"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "64"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "65536"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "67108864"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "8192"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "8388608"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 64, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0001-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0002-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0003-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0004-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0005-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0006-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1024"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 173, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1048576"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 125471, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "128"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 8, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "131072"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 22293, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "134217728"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 8.127518e+06, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "16"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 2, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "16384"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 2566, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "16777216"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 1.598283e+06, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "2048"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 336, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "2097152"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 241574, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "256"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 35, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "262144"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 45678, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "32"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 5, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "32768"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 5268, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "33554432"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 2.274739e+06, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "4"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "4096"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 669, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "4194304"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 476715, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "512"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 49, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "524288"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 64199, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "64"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 8, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "65536"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 10796, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "67108864"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 4.270417e+06, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 0, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "8192"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 1319, false},
		{"lustre_rpcs_offset", "Current RPC offset by size.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "8388608"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}}, 942102, false},
		{"lustre_write_bytes_total", "The total number of bytes that have been written.", counter, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 9.381379729408e+13, false},
		{"lustre_free_kilobytes", "Number of kilobytes allocated to the pool", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 2.83007085568e+11, false},
		{"lustre_inodes_free", "The number of inodes (objects) available", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 4.30405267e+08, false},
		{"lustre_capacity_kilobytes", "Capacity of the pool in kilobytes", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 2.83010362368e+11, false},
		{"lustre_default_ea_size_bytes", "Default Extended Attribute (EA) size in bytes", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 128, false},
		{"lustre_maximum_read_ahead_whole_megabytes", "Maximum file size in megabytes for a file to be read in its entirety", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 2, false},
		{"lustre_maximum_read_ahead_per_file_megabytes", "Maximum number of megabytes per file to read ahead", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 64, false},
		{"lustre_statahead_maximum", "Maximum window size for statahead", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 32, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "alloc_inode"}, {"target", "lustrefs-ffff88105db50000"}}, 2, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "close"}, {"target", "lustrefs-ffff88105db50000"}}, 96, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "getattr"}, {"target", "lustrefs-ffff88105db50000"}}, 41, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "getxattr"}, {"target", "lustrefs-ffff88105db50000"}}, 85, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "inode_permission"}, {"target", "lustrefs-ffff88105db50000"}}, 398, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "open"}, {"target", "lustrefs-ffff88105db50000"}}, 136, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "readdir"}, {"target", "lustrefs-ffff88105db50000"}}, 12, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "removexattr"}, {"target", "lustrefs-ffff88105db50000"}}, 134, false},
		{"lustre_stats_total", "Number of operations the filesystem has performed.", counter, []labelPair{{"component", "client"}, {"operation", "truncate"}, {"target", "lustrefs-ffff88105db50000"}}, 134, false},
		{"lustre_write_maximum_size_bytes", "The maximum write size in bytes.", gauge, []labelPair{{"component", "client"}, {"target", "lustrefs-ffff88105db50000"}}, 1.048576e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0001-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0002-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0003-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0004-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0005-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "0"}, {"target", "lustrefs-OST0006-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 90, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "1"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "10"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "11"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "12"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "13"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "14"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "15"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 13, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "2"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "3"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 12, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "3"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 11, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "4"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "5"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 9, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "5"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "6"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 8, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "6"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "7"}, {"target", "lustrefs-MDT0000-mdc-ffff88105db50000"}, {"type", "mdc"}}, 53, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "7"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "8"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "read"}, {"size", "9"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0001-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0002-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0003-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0004-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0005-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "0"}, {"target", "lustrefs-OST0006-osc-ffff88105db50000"}, {"type", "osc"}}, 0, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "1"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 1.053804e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "10"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 91419, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "11"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 18683, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "12"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 2399, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "13"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 174, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "14"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 5, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "15"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 2, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "2"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 3.518152e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "3"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 4.520192e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "4"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 3.656921e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "5"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 2.35005e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "6"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 1.396192e+06, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "7"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 832325, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "8"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 497409, false},
		{"lustre_rpcs_in_flight", "Current number of RPCs that are processing during the snapshot.", gauge, []labelPair{{"component", "client"}, {"operation", "write"}, {"size", "9"}, {"target", "lustrefs-OST0000-osc-ffff88105db50000"}, {"type", "osc"}}, 272560, false},

		// Generic Metrics
		{"lustre_cache_miss_total", "Total number of cache misses.", counter, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_cache_access_total", "Total number of times cache has been accessed.", counter, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_free_pages", "Current number of pages available.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_maximum_pools", "Number of pools.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 4009, false},
		{"lustre_pages_in_pools", "Number of pages in all pools.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_pages_per_pool", "Number of pages per pool.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 512, false},
		{"lustre_maximum_pages_reached_total", "Total number of pages reached.", counter, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_maximum_pages", "Maximum number of pages that can be held.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 2.052111e+06, false},
		{"lustre_physical_pages", "Capacity of physical memory.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 1.641689e+07, false},
		{"lustre_grows_total", "Total number of grows.", counter, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_maximum_waitqueue_depth", "Maximum waitqueue length.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_grows_failure_total", "Total number of failures while attempting to add pages.", counter, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_shrinks_total", "Total number of shrinks.", counter, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_free_page_low", "Lowest number of free pages reached.", gauge, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},
		{"lustre_out_of_memory_request_total", "Total number of out of memory requests.", 0, []labelPair{{"component", "generic"}, {"target", "sptlrpc"}}, 0, false},

		// LNET Metrics
		{"lustre_console_max_delay_centiseconds", "Minimum time in centiseconds before the console logs a message", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 60000, false},
		{"lustre_drop_bytes_total", "Total number of bytes that have been dropped", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_maximum", "Maximum number of outstanding messages", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 28, false},
		{"lustre_route_count_total", "Total number of messages that have been routed", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_console_backoff_enabled", "Returns non-zero number if console_backoff is enabled", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 2, false},
		{"lustre_receive_count_total", "Total number of messages that have been received", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 1.01719291e+08, false},
		{"lustre_console_min_delay_centiseconds", "Maximum time in centiseconds before the console logs a message", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 50, false},
		{"lustre_receive_bytes_total", "Total number of bytes received", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 5.302956535372e+13, false},
		{"lustre_send_count_total", "Total number of messages that have been sent", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 1.01719323e+08, false},
		{"lustre_allocated", "Number of messages currently allocated", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_catastrophe_enabled", "Returns 1 if currently in catastrophe mode", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_errors_total", "Total number of errors", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_lnet_memory_used_bytes", "Number of bytes allocated by LNET", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 3.6109496e+07, false},
		{"lustre_send_bytes_total", "Total number of bytes sent", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 2.1201322992e+10, false},
		{"lustre_route_bytes_total", "Total number of bytes for routed messages", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_fail_error_total", "Number of errors that have been thrown", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_watchdog_ratelimit_enabled", "Returns 1 if the watchdog rate limiter is enabled", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 300, false},
		{"lustre_console_ratelimit_enabled", "Returns 1 if the console message rate limiting is enabled", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 1, false},
		{"lustre_debug_megabytes", "Maximum buffer size in megabytes for the LNET debug messages", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 101, false},
		{"lustre_drop_count_total", "Total number of messages that have been dropped", counter, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_fail_maximum", "Maximum number of times to fail", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 0, false},
		{"lustre_panic_on_lbug_enabled", "Returns 1 if panic_on_lbug is enabled", gauge, []labelPair{{"component", "lnet"}, {"target", "lnet"}}, 1, false},

		//Health metrics
		{"lustre_health_check", "Current health status for the indicated instance: 1 refers to 'healthy', 0 refers to 'unhealthy'", gauge, []labelPair{{"component", "health"}, {"target", "lustre"}}, 1, false},
	}

func TestCollector(t *testing.T) {
	sources.CollectVersion = "v2"
	sources.SHELF_LIFE = time.Duration(0)

	targets := []string{"OST", "MDT", "MGS", "MDS", "Client", "Generic", "LNET", "Health"}
	//targets := []string{"OST", "MDT"}
	// Override the default file location to the local proc directory
	sources.ProcLocation = "proc"
	sources.SysLocation = "sys"

	// These following metrics should be filtered out as they are specific to the deployment and will always change
	blacklistedMetrics := []string{"go_", "http_", "process_", "lustre_exporter_", "promhttp_"}

	for i, metric := range expectedMetrics {
		newLabels, err := sortByKey(metric.Labels)
		if err != nil {
			t.Fatal(err)
		}
		expectedMetrics[i].Labels = newLabels
	}

	numParsed := 0
	for _, target := range targets {
		toggleCollectors(target)
		var missingMetrics []promType // Array of metrics that are missing for the given target
		var valNotEqualMetrics []promType
		enabledSources := []string{"procfs", "procsys", "sysfs"}

		sourceList, err := loadSources(enabledSources)
		if err != nil {
			t.Fatal("Unable to load sources")
		}
		if err = prometheus.Register(LustreSource{sourceList: sourceList}); err != nil {
			t.Fatalf("Failed to register for target: %s", target)
		}
		handler := promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{ErrorLog: log.NewErrorLogger(), ErrorHandling: promhttp.ContinueOnError})
		promServer := httptest.NewServer(promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, handler))
		defer promServer.Close()

		resp, err := http.Get(promServer.URL)
		if err != nil {
			t.Fatalf("Failed to GET data from prometheus: %v", err)
		}

		if resp.StatusCode != 200 {
			buf, _ := io.ReadAll(resp.Body)
			t.Fatalf("%d: %s", resp.StatusCode, buf)
		}

		var parser expfmt.TextParser


		buf, _ := io.ReadAll(resp.Body)
		resp.Body.Read(buf)
		//t.Logf("%s", buf)
		metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(buf))
		if err != nil {
			t.Fatal(err)
		}
		err = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		for _, metricFamily := range metricFamilies {
			if blacklisted(blacklistedMetrics, *metricFamily.Name) {
				continue
			}
			for _, metric := range metricFamily.Metric {
				var value float64
				if *metricFamily.Type == counter {
					value = *metric.Counter.Value
				} else if *metricFamily.Type == gauge {
					value = *metric.Gauge.Value
				} else if *metricFamily.Type == untyped {
					value = *metric.Untyped.Value
				}
				var labels []labelPair
				for _, label := range metric.Label {
					l := labelPair{
						Name:  *label.Name,
						Value: *label.Value,
					}
					labels = append(labels, l)
				}
				p := promType{
					Name:   *metricFamily.Name,
					Help:   *metricFamily.Help,
					Type:   int(*metricFamily.Type),
					Labels: labels,
					Value:  value,
				}

				// Check if exists here
				expectedMetrics, err = compareResults(p, expectedMetrics)
				if err == errMetricNotFound {
					missingMetrics = append(missingMetrics, p)
				}else if err == errMetricValueNotEqual {
					valNotEqualMetrics = append(valNotEqualMetrics, p)
				}else if err == errMetricAlreadyParsed {
					t.Fatalf("Retrieved an unexpected duplicate of %s metric: %+v", target, p)
				}
				numParsed++
			}
		}
		if len(missingMetrics) != 0 {
			t.Fatalf("The following %s metrics were not found in expects: %v", target, missingMetrics)
		}
		if len(valNotEqualMetrics) != 0 {
			t.Fatalf("The following %s metrics'val is not equal from expects: %v", target, valNotEqualMetrics)
		}

		prometheus.Unregister(LustreSource{sourceList: sourceList})
	}

	if l := len(expectedMetrics); l != numParsed {
		t.Fatalf("Retrieved an unexpected number of metrics. Expected: %d, Got: %d", l, numParsed)
	}

	// Return the proc location to the default value
	sources.ProcLocation = "/proc"
	sources.SysLocation = "/sys"
}
