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

package sources

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type prometheusType func([]string, []string, string, string, uint64) prometheus.Metric

type lustreProcMetric struct {
	filename   string
	promName   string
	source     string //The parent data source (OST, MDS, MGS, etc)
	path       string //Path to retrieve metric from
	helpText   string
	metricFunc prometheusType
}

type lustreStatsMetric struct {
	title string
	help  string
	value uint64
}

type lustreHelpStruct struct {
	filename   string
	promName   string // Name to be used in Prometheus
	helpText   string
	metricFunc prometheusType
}

func newLustreProcMetric(filename string, promName string, source string, path string, helpText string, metricFunc prometheusType) lustreProcMetric {
	var m lustreProcMetric
	m.filename = filename
	m.promName = promName
	m.source = source
	m.path = path
	m.helpText = helpText
	m.metricFunc = metricFunc

	return m
}

func regexCaptureString(pattern string, textToMatch string) (matchedString string) {
	// Return the first string in a list of matched strings if found
	strings := regexCaptureStrings(pattern, textToMatch)
	if len(strings) < 1 {
		return ""
	}
	return strings[0]
}

func regexCaptureStrings(pattern string, textToMatch string) (matchedStrings []string) {
	re := regexp.MustCompile(pattern)
	matchedStrings = re.FindAllString(textToMatch, -1)
	return matchedStrings
}

func parseFileElements(path string, directoryDepth int) (name string, nodeName string, err error) {
	pathElements := strings.Split(path, "/")
	pathLen := len(pathElements)
	if pathLen < 1 {
		return "", "", fmt.Errorf("path did not return at least one element")
	}
	name = pathElements[pathLen-1]
	nodeName = pathElements[pathLen-2-directoryDepth]
	nodeName = strings.TrimPrefix(nodeName, "filter-")
	nodeName = strings.TrimSuffix(nodeName, "_UUID")
	return name, nodeName, nil
}