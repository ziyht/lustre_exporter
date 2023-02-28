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
	"fmt"
	"net/http"
	"os"

	_ "net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"gopkg.in/alecthomas/kingpin.v2"

	"lustre_exporter/log"
	"lustre_exporter/sources"
)

var (
	scrapeDurations = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Namespace: sources.Namespace,
			Subsystem: "exporter",
			Name:      "scrape_duration_seconds",
			Help:      "lustre_exporter: Duration of a scrape job.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.95: 0.005, 0.99: 0.001},
		},
		[]string{"source", "result"},
	)
)


var MaxMultiRun = 4

//LustreSource is a list of all sources that the user would like to collect.
type LustreSource struct {
	sourceList map[string]sources.LustreSource
}

//Describe implements the prometheus.Describe interface
func (l LustreSource) Describe(ch chan<- *prometheus.Desc) {
	scrapeDurations.Describe(ch)
}

//Collect implements the prometheus.Collect interface
func (l LustreSource) Collect(ch chan<- prometheus.Metric) {
	sources.Runner().Update(l.sourceList, scrapeDurations, ch)
}

func loadSources(list []string) (map[string]sources.LustreSource, error) {
	sourceList := map[string]sources.LustreSource{}
	for _, name := range list {
		fn, ok := sources.Factories[name]
		if !ok {
			return nil, fmt.Errorf("source %q not available", name)
		}
		c := fn()
		sourceList[name] = c
	}
	return sourceList, nil
}

func init() {
	prometheus.MustRegister(version.NewCollector("lustre_exporter"))
}

func main() {
	kingpin.Version(version.Print("lustre_exporter"))
	kingpin.HelpFlag.Short('h')

	var (
		clientEnabled       = kingpin.Flag("collector.client", "Set client metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		genericEnabled      = kingpin.Flag("collector.generic", "Set generic metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		lnetEnabled         = kingpin.Flag("collector.lnet", "Set LNET metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		mdsEnabled          = kingpin.Flag("collector.mds", "Set MDS metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		mdtEnabled          = kingpin.Flag("collector.mdt", "Set MDT metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		mgsEnabled          = kingpin.Flag("collector.mgs", "Set MGS metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		ostEnabled          = kingpin.Flag("collector.ost", "Set OST metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		healthStatusEnabled = kingpin.Flag("collector.health", "Set Health metric level. Valid levels: [extended, core, disabled]").Default("extended").Enum("extended", "core", "disabled")
		listenAddress       = kingpin.Flag("web.listen-address", "Address to use to expose Lustre metrics.").Default(":9169").String()
		metricsPath         = kingpin.Flag("web.telemetry-path", "Path to use to expose Lustre metrics.").Default("/metrics").String()

		procPath            = kingpin.Flag("collector.path.proc", "Path to collect data from proc").Default("/proc").String()
		sysPath             = kingpin.Flag("collector.path.sys" , "Path to collect data from sys").Default("/sys").String()
		collectVer          = kingpin.Flag("collector.collect.ver" , "collect version").Default("v2").String()
		workers             = kingpin.Flag("collector.v2.workers", "max collecting workers can create in the same time").Default("4").Int()
		shelflife           = kingpin.Flag("collector.v2.shelflife", "data shelf life, no repeated collection during the shelf life").Default("1s").Duration()
	)

	kingpin.Parse()

	log.Infoln("Starting lustre_exporter", version.Info())
	log.Infoln("Build context", version.BuildContext())

	log.Infof("Collector status:")
	sources.OstEnabled = *ostEnabled
	log.Infof(" - OST State: %s", sources.OstEnabled)
	sources.MdtEnabled = *mdtEnabled
	log.Infof(" - MDT State: %s", sources.MdtEnabled)
	sources.MgsEnabled = *mgsEnabled
	log.Infof(" - MGS State: %s", sources.MgsEnabled)
	sources.MdsEnabled = *mdsEnabled
	log.Infof(" - MDS State: %s", sources.MdsEnabled)
	sources.ClientEnabled = *clientEnabled
	log.Infof(" - Client State: %s", sources.ClientEnabled)
	sources.GenericEnabled = *genericEnabled
	log.Infof(" - Generic State: %s", sources.GenericEnabled)
	sources.LnetEnabled = *lnetEnabled
	log.Infof(" - Lnet State: %s", sources.LnetEnabled)
	sources.HealthStatusEnabled = *healthStatusEnabled
	log.Infof(" - Health State: %s", sources.HealthStatusEnabled)
	sources.ProcLocation = *procPath
	log.Infof(" - Proc Path: %s", sources.ProcLocation)
	sources.SysLocation = *sysPath
	log.Infof(" - Sys  Path: %s", sources.SysLocation)
	sources.CollectVersion = *collectVer
	if sources.CollectVersion != "v2"{
		sources.CollectVersion = "v1"
	}
	log.Infof(" - Collect Ver: %s", sources.CollectVersion)

	sources.MAX_WORKER = *workers
	if sources.MAX_WORKER <= 0 {
		sources.MAX_WORKER = 4
	}
	log.Infof(" - V2 Max Worker : %d", sources.MAX_WORKER)

	sources.SHELF_LIFE = *shelflife
	log.Infof(" - V2 Shelf Life : %s", sources.SHELF_LIFE)

	enabledSources := []string{"procfs", "procsys", "sysfs"}

	sourceList, err := loadSources(enabledSources)
	if err != nil {
		log.Fatalf("Couldn't load sources: %q", err)
	}

	log.Infof("Available sources:")
	for s := range sourceList {
		log.Infof(" - %s", s)
	}

	prometheus.MustRegister(LustreSource{sourceList: sourceList})
	handler := promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{ErrorLog: log.NewErrorLogger(), ErrorHandling: promhttp.ContinueOnError})

	http.Handle(*metricsPath, promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, handler))
	http.HandleFunc("/-/exit", func(w http.ResponseWriter, r *http.Request){
		log.Infof("Exit(1) on remote call")
		os.Exit(1)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var num int
		num, err = w.Write([]byte(`<html>
			<head><title>Lustre Exporter</title></head>
			<body>
			<h1>Lustre Exporter</h1>
			<p><a href="` + *metricsPath + `">Metrics</a></p>
			</body>
			</html>`))
		if err != nil {
			log.Fatal(num, err)
		}
	})

	log.Infoln("Listening on", *listenAddress)
	err = http.ListenAndServe(*listenAddress, nil)
	if err != nil {
		log.Fatal(err)
	}
}
