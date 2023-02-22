package sources

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unsafe"

	"lustre_exporter/log"

	"github.com/prometheus/client_golang/prometheus"
)

type procfsV2 struct {
	reNum *regexp.Regexp
}

type procfsV2Ctx struct {
  s                  *lustreProcfsSource
	fr                 *fileReader
	filesJobStats      map[string]*[]jobState
	//lastover           time.Time
	metrics_           []prometheus.Metric
}

var insProcfsV2 = &procfsV2{}

func (v2 *procfsV2)newCtx(s  *lustreProcfsSource) *procfsV2Ctx{
	return &procfsV2Ctx{
	  s            : s,
		fr           : newFileReader(),
		filesJobStats: map[string]*[]jobState{},
	}
}


func (ctx *procfsV2Ctx)update(ch chan<- prometheus.Metric) {
	for _, m := range ctx.metrics_ {
		ch <- m
	}
}

func (ctx *procfsV2Ctx)release()  {
	for _, jss := range ctx.filesJobStats{
		sPool.recycleJobStates(jss)
	}

	ctx.filesJobStats = nil

	ctx.fr.release()
}

func (ctx *procfsV2Ctx)prepareFiles() (err error) {
	for _, metric := range ctx.s.lustreProcMetrics {
		_, err := ctx.fr.glob(filepath.Join(ctx.s.basePath, metric.path, metric.filename), true)
		if err != nil {
			return err
		}
	}
	ctx.fr.wait(true)

	return nil
}

func (ctx *procfsV2Ctx)collect() error {
	var metricType string
	var directoryDepth int

	s := ctx.s

	ctx.prepareFiles()

	var metrics []prometheus.Metric

	for _, metric := range s.lustreProcMetrics {
		directoryDepth = strings.Count(metric.filename, "/")
		paths, err := ctx.fr.glob(filepath.Join(s.basePath, metric.path, metric.filename))
		if err != nil {
			return err
		}
		if paths == nil {
			continue
		}
		for _, path := range paths {
			metricType = single
			switch metric.filename {
			case "brw_stats", "rpc_stats":
				err = ctx.parseBRWStats(metric.source, "stats", path, directoryDepth, metric.helpText, metric.promName, metric.hasMultipleVals, func(nodeType string, brwOperation string, brwSize string, nodeName string, name string, helpText string, value float64, extraLabel string, extraLabelValue string) {
					if extraLabelValue == "" {
						metrics = append(metrics, metric.metricFunc([]string{"component", "target", "operation", "size"}, []string{nodeType, nodeName, brwOperation, brwSize}, name, helpText, value))
					} else {
						metrics = append(metrics, metric.metricFunc([]string{"component", "target", "operation", "size", extraLabel}, []string{nodeType, nodeName, brwOperation, brwSize, extraLabelValue}, name, helpText, value))
					}
				})
				if err != nil {
					return err
				}
			case "job_stats":
				err = ctx.parseJobStats(metric.source, "job_stats", path, directoryDepth, metric.helpText, metric.promName, metric.hasMultipleVals, func(nodeType string, jobid string, nodeName string, name string, helpText string, value float64, extraLabel string, extraLabelValue string) {
					if extraLabelValue == "" {
						metrics = append(metrics, metric.metricFunc([]string{"component", "target", "jobid"}, []string{nodeType, nodeName, jobid}, name, helpText, value))
					} else {
						metrics = append(metrics, metric.metricFunc([]string{"component", "target", "jobid", extraLabel}, []string{nodeType, nodeName, jobid, extraLabelValue}, name, helpText, value))
					}
				})
				if err != nil {
					return err
				}
			default:
				if metric.filename == stats {
					metricType = stats
				} else if metric.filename == mdStats {
					metricType = mdStats
				} else if metric.filename == encryptPagePools {
					metricType = encryptPagePools
				}
				err = ctx.parseFile(metric.source, metricType, path, directoryDepth, metric.helpText, metric.promName, metric.hasMultipleVals, func(nodeType string, nodeName string, name string, helpText string, value float64, extraLabel string, extraLabelValue string) {
					if extraLabelValue == "" {
						metrics = append(metrics, metric.metricFunc([]string{"component", "target"}, []string{nodeType, nodeName}, name, helpText, value))
					} else {
						metrics = append(metrics, metric.metricFunc([]string{"component", "target", extraLabel}, []string{nodeType, nodeName, extraLabelValue}, name, helpText, value))
					}
				})
				if err != nil {
					return err
				}
			}
		}
	}

	ctx.metrics_ = metrics

	return nil
}

var	brwStatsMetricBlocks = map[string]string{
		pagesPerBlockRWHelp:    "pages per bulk r/w",
		discontiguousPagesHelp: "discontiguous pages",
		diskIOsInFlightHelp:    "disk I/Os in flight",
		ioTimeHelp:             "I/O time",
		diskIOSizeHelp:         "disk I/O size",
		pagesPerRPCHelp:        "pages per rpc",
		rpcsInFlightHelp:       "rpcs in flight",
		offsetHelp:             "offset",
	}

func (ctx *procfsV2Ctx) parseBRWStats(nodeType string, metricType string, path string, directoryDepth int, helpText string, promName string, hasMultipleVals bool, handler func(string, string, string, string, string, string, float64, string, string)) (err error) {
	_, nodeName, err := parseFileElements(path, directoryDepth)
	if err != nil {
		return err
	}

	statsFileBytes, err := ctx.fr.readFile(path)
	if err != nil {
		return err
	}
	statsFile := string(statsFileBytes[:])
	block := regexCaptureString("(?ms:^"+brwStatsMetricBlocks[helpText]+".*?(\n\n|\\z))", statsFile)
	metricList, err := splitBRWStats(block)
	if err != nil {
		return err
	}
	extraLabel := ""
	extraLabelValue := ""
	if hasMultipleVals {
		extraLabel = "type"
		pathElements := strings.Split(path, "/")
		extraLabelValue = pathElements[len(pathElements)-3]
	}
	for _, item := range metricList {
		value, err := strconv.ParseFloat(item.value, 64)
		if err != nil {
			return err
		}
		handler(nodeType, item.operation, convertToBytes(item.size), nodeName, promName, helpText, value, extraLabel, extraLabelValue)
	}
	return nil
}

func (ctx *procfsV2Ctx) parseFile(nodeType string, metricType string, path string, directoryDepth int, helpText string, promName string, hasMultipleVals bool, handler func(string, string, string, string, float64, string, string)) (err error) {
	_, nodeName, err := parseFileElements(path, directoryDepth)
	if err != nil {
		return err
	}
	switch metricType {
	case single:
		value, err := ctx.fr.readFile(filepath.Clean(path))
		if err != nil {
			return err
		}
		convertedValue, err := strconv.ParseFloat(strings.TrimSpace(string(value)), 64)
		if err != nil {
			return err
		}
		handler(nodeType, nodeName, promName, helpText, convertedValue, "", "")
	case stats, mdStats, encryptPagePools:
		metricList, err := ctx.parseStatsFile(helpText, promName, path, hasMultipleVals)
		if err != nil {
			return err
		}

		for _, metric := range metricList {
			handler(nodeType, nodeName, metric.title, metric.help, metric.value, metric.extraLabel, metric.extraLabelValue)
		}
	}
	return nil
}

func (ctx *procfsV2Ctx)parseStatsFile(helpText string, promName string, path string, hasMultipleVals bool) (metricList []lustreStatsMetric, err error) {
	statsFileBytes, err := ctx.fr.readFile(path)
	if err != nil {
		return nil, err
	}
	statsFile := string(statsFileBytes[:])
	var statsList []lustreStatsMetric
	if hasMultipleVals {
		statsList, err = getStatsOperationMetrics(statsFile, promName, helpText)
	} else {
		statsList, err = getStatsIOMetrics(statsFile, promName, helpText)
	}
	if err != nil {
		return nil, err
	}
	if statsList != nil {
		metricList = append(metricList, statsList...)
	}

	return metricList, nil
}

func (ctx *procfsV2Ctx) parseJobStats(nodeType string, metricType string, path string, directoryDepth int, helpText string, promName string, hasMultipleVals bool, handler func(string, string, string, string, string, float64, string, string)) (err error) {
	_, nodeName, err := parseFileElements(path, directoryDepth)
	if err != nil {
		return err
	}

	metricList, err := ctx.parseJobStatsText(path, promName, helpText, hasMultipleVals)
	if err != nil {
		return err
	}

	for _, item := range metricList {
		handler(nodeType, item.jobID, nodeName, item.lustreStatsMetric.title, item.lustreStatsMetric.help, item.lustreStatsMetric.value, item.lustreStatsMetric.extraLabel, item.lustreStatsMetric.extraLabelValue)
	}
	return nil
}

type jobState struct {
  jobid           string
	readbytes       [4]int64   `key:"read_bytes"`
	writebytes      [4]int64   `key:"write_bytes"`
	open            int64      `key:"open"`
	close           int64      `key:"close"`
	mknod           int64      `key:"mknod"`
	link            int64      `key:"link"`
	unlink          int64      `key:"unlink"`
	mkdir           int64      `key:"mkdir"`
	rmdir           int64      `key:"rmdir"`
	rename          int64      `key:"rename"`
	getattr         int64      `key:"getattr"`
	setattr         int64      `key:"setattr"`
	getxattr        int64      `key:"getxattr"`
	setxattr        int64      `key:"setxattr"`
	statfs          int64      `key:"statfs"`
	sync            int64      `key:"sync"`
	samedir_rename  int64      `key:"samedir_rename"`
	crossdir_rename int64      `key:"crossdir_rename"`
	punch           int64      `key:"punch"`
	destroy         int64      `key:"destroy"`
	create          int64      `key:"create"`
	get_info        int64      `key:"get_info"`
	set_info        int64      `key:"set_info"`
	quotactl        int64      `key:"quotactl"`
}

func newJobState() any{
	return new(jobState)
}

func newJobStates() any{
	out := make([]jobState, 0, 512)
	return &out
}

var jobStateInitVal jobState

func (js *jobState)__init(){
	len := int(unsafe.Sizeof(*js)) / 8 - 2
	js.jobid = ""
	for i := 0; i < len; i++ {
		*(*int64)(unsafe.Pointer(uintptr(unsafe.Pointer(&js.readbytes)) + uintptr(i * 8))) = -1
	}
}

func (js *jobState)__init2(){
	*js = jobStateInitVal
}

func (js *jobState)parsingFromText(content string)error{
	js.__init2()

	lines := strings.Split(content, "\n")

	if len(lines) < 1 {
		return fmt.Errorf("invalid content for parsing jobStat: %s", content)
	}

	jobid, err := getJobID(lines[0])
	if err != nil {
		return fmt.Errorf("can not found jobid in content: %s", content)
	}
	
	js.jobid = jobid

	for _, line := range lines[1:] {
		idx := strings.Index(line, ":")
		if idx < 1{
			continue
		}

		var cnt int
		key := strings.TrimSpace(line[:idx])
		switch key {
			case "read_bytes"     : cnt, err = parsingNums(&js.readbytes , line); if cnt <= 3 { err = fmt.Errorf("invalid data for %s", key) }
			case "write_bytes"    : cnt, err = parsingNums(&js.writebytes, line); if cnt <= 3 { err = fmt.Errorf("invalid data for %s", key) }
			case "open"           : err= parsingNumAt(&js.open ,    line)
			case "close"          : err= parsingNumAt(&js.close,    line) 
			case "mknod"          : err= parsingNumAt(&js.mknod,    line) 
			case "link"           : err= parsingNumAt(&js.link,     line) 
			case "unlink"         : err= parsingNumAt(&js.unlink,   line) 
			case "mkdir"          : err= parsingNumAt(&js.mkdir,    line) 
			case "rmdir"          : err= parsingNumAt(&js.rmdir,    line) 
			case "rename"         : err= parsingNumAt(&js.rename,   line) 
			case "getattr"        : err= parsingNumAt(&js.getattr,  line) 
			case "setattr"        : err= parsingNumAt(&js.setattr,  line) 
			case "getxattr"       : err= parsingNumAt(&js.getxattr, line) 
			case "setxattr"       : err= parsingNumAt(&js.setxattr, line) 
			case "statfs"         : err= parsingNumAt(&js.statfs,   line) 
			case "sync"           : err= parsingNumAt(&js.sync,     line) 
			case "samedir_rename" : err= parsingNumAt(&js.samedir_rename,  line) 
			case "crossdir_rename": err= parsingNumAt(&js.crossdir_rename, line) 
			case "punch"          : err= parsingNumAt(&js.punch,    line) 
			case "destroy"        : err= parsingNumAt(&js.destroy,  line) 
			case "create"         : err= parsingNumAt(&js.create,   line) 
			case "get_info"       : err= parsingNumAt(&js.get_info, line) 
			case "set_info"       : err= parsingNumAt(&js.set_info, line) 
			case "quotactl"       : err= parsingNumAt(&js.quotactl, line) 
		} 
		if err != nil {
			return fmt.Errorf("parsing failed for key '%s' of jobid '%s', line is: %s", key, jobid, line)
		}
	} 

	return  nil
}

func parsingNums(dest *[4]int64, input string)(int, error){
	numStrs := insProcfsV2.reNum.FindAllString(input, -1)
	cnt := 0
	for _, numStr := range numStrs {
		num, err := strconv.ParseInt(strings.TrimSpace(numStr), 10, 64)
		if err != nil {
			return cnt, err
		}
		dest[cnt] = num
		cnt += 1
		if cnt == 4 {
			break
		}
	}

	return cnt, nil
}

func parsingNumAt(dest *int64, input string)(error){
	numStrs := insProcfsV2.reNum.FindAllString(input, 1)
	if len(numStrs) < 1 {
		return fmt.Errorf("can not find any num strings")
	}

	num, err := strconv.ParseInt(strings.TrimSpace(numStrs[0]), 10, 64)
	if err != nil {
		return err
	}
	*dest = num

	return nil
}

func (ctx *procfsV2Ctx)parseJobStatsText(path string, promName string, helpText string, hasMultipleVals bool) (metricList []lustreJobsMetric, err error){

	var jobList []lustreJobsMetric

	jobsStats, ok := ctx.filesJobStats[path]
	if !ok {
		jobsStats = sPool.newJobStates()
		jobStatsBytes, err := ctx.fr.readFile(path)
		if err != nil {
			return nil, err
		}
		jobStatsContent := string(jobStatsBytes[:])
		splits := strings.Split(jobStatsContent, "- ")
		if len(splits) <= 1{
			return nil, nil
		}
		jobs := splits[1:]

		var js jobState
		for _, job := range jobs {
			err = js.parsingFromText(job)
			if err != nil {
				log.Warnf("parsing jobstat failed: %s", err)
				continue
			}
			*jobsStats = append(*jobsStats, js)
		}
		ctx.filesJobStats[path] = jobsStats
	}

	if hasMultipleVals {
		jobList, _ = ctx.getJobStatsOperationMetrics(*jobsStats, promName, helpText)
	} else {
		jobList, _ = ctx.getJobStatsIOMetrics(*jobsStats, promName, helpText)
	}
	if jobList != nil {
		metricList = jobList
	}
	return metricList, nil
}

func getJobID(line string) (string, error) {

	idx := strings.Index(line, "job_id:")
	if idx < 0 {
		return "", fmt.Errorf("not found")
	}

	idx2 := strings.Index(line[idx:], "\n")
	if idx2 < 0 {
		return getValidUtf8String(strings.TrimSpace(line[idx+len("job_id:"):])), nil
	}
	return getValidUtf8String(strings.TrimSpace(line[idx+len("job_id:"):idx2])), nil
}

func (ctx *procfsV2Ctx)parsingJobStats(job string) (string, map[string][]int64, error) {

	lines := strings.Split(job, "\n")

	if len(lines) < 1 {
		return "", nil, fmt.Errorf("invalid content for parsing jobStat: %s", job)
	}

	jobid, err := getJobID(lines[0])
	if err != nil {
		return "", nil, fmt.Errorf("can not found jobid in content: %s", job)
	}

	j := map[string][]int64{}
	for _, line := range lines[1:] {

		idx := strings.Index(line, ":")
		if idx < 1{
			continue
		}

		key := strings.TrimSpace(line[:idx])
		numStrs := insProcfsV2.reNum.FindAllString(line[idx:], -1)
		nums := make([]int64, 0, 1)
		for _, numStr := range numStrs {
			num, err := strconv.ParseInt(strings.TrimSpace(numStr), 10, 64)
			if err != nil {
				continue
			}
			nums = append(nums, num)
		}

		j[key] = nums
	} 

	return jobid, j, nil
}

func (ctx *procfsV2Ctx)getJobStatsOperationMetrics(jobsStats []jobState, promName string, helpText string) (metricList []lustreJobsMetric, err error) {

	for _, js := range jobsStats {
		var typeInfo = reflect.TypeOf(js)
    var valInfo = reflect.ValueOf(js)
    num := typeInfo.NumField()
		for i := 0; i < num; i++ {
			key := typeInfo.Field(i).Tag.Get("key")
			
			if key == "" || key == "read_bytes" || key == "write_bytes" {
				continue
			}
			val := valInfo.Field(i).Int()
			if val < 0 {
				continue
			}

			metricList = append(metricList, lustreJobsMetric{js.jobid, lustreStatsMetric{
				title:           promName,
				help:            helpText,
				value:           float64(val),
				extraLabel:      "operation",
				extraLabelValue: key,
			}})
		}
	}

	return metricList, err
}

var jobStatsMultistatParsingStruct map[string]multistatParsingStruct = map[string]multistatParsingStruct{
		readSamplesHelp:  {index: 0, pattern: "read_bytes"},
		readMinimumHelp:  {index: 1, pattern: "read_bytes"},
		readMaximumHelp:  {index: 2, pattern: "read_bytes"},
		readTotalHelp:    {index: 3, pattern: "read_bytes"},
		writeSamplesHelp: {index: 0, pattern: "write_bytes"},
		writeMinimumHelp: {index: 1, pattern: "write_bytes"},
		writeMaximumHelp: {index: 2, pattern: "write_bytes"},
		writeTotalHelp:   {index: 3, pattern: "write_bytes"},
	}

func (ctx *procfsV2Ctx)getJobStatsIOMetrics(jobsStats []jobState, promName string, helpText string) (metricList []lustreJobsMetric, err error){
	// opMap matches the given helpText value with the placement of the numeric fields within each metric line.
	// For example, the number of samples is the first number in the line and has a helpText of readSamplesHelp,
	// hence the 'index' value of 0. 'pattern' is the regex capture pattern for the desired line.
	opMap := jobStatsMultistatParsingStruct
	// If the metric isn't located in the map, don't try to parse a value for it.
	if _, exists := opMap[helpText]; !exists {
		return nil, nil
	}

	operation := opMap[helpText]
	for _, js := range jobsStats {
		if operation.pattern == "read_bytes" {
			l := lustreStatsMetric{
				title:           promName,
				help:            helpText,
				value:           float64(js.readbytes[operation.index]),
				extraLabel:      "",
				extraLabelValue: "",
			}

			metricList = append(metricList, lustreJobsMetric{js.jobid, l})
		}
		if operation.pattern == "write_bytes" {
			l := lustreStatsMetric{
				title:           promName,
				help:            helpText,
				value:           float64(js.writebytes[operation.index]),
				extraLabel:      "",
				extraLabelValue: "",
			}
			metricList = append(metricList, lustreJobsMetric{js.jobid, l})
		}
	}

	return 
}

func init(){
	insProcfsV2.reNum = regexp.MustCompile(`[0-9]*\.[0-9]+|[0-9]+`)
	jobStateInitVal.__init()
}