package sources

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unsafe"

	"lustre_exporter/log"

	"github.com/prometheus/client_golang/prometheus"
)

type procfsV2 struct {
	reNum   *regexp.Regexp
	reSpace *regexp.Regexp
}

type procfsV2Ctx struct {
  s                  *lustreProcfsSource
	fr                 *fileReader
	filesJobStats      map[string]*[]jobState
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
			  basicLables := []string{"component", "target", "operation", "size"}
				err = ctx.parseBRWStats(metric.source, "stats", path, directoryDepth, &metric, basicLables)
				if err != nil {
					return err
				}
			case "job_stats":
				basicLables := []string{"component", "target", "jobid"}
				err = ctx.parseJobStats(metric.source, "job_stats", path, directoryDepth, &metric, basicLables)
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
				basicLables := []string{"component", "target"}
				err = ctx.parseFile(metric.source, metricType, path, directoryDepth, &metric, basicLables)
				if err != nil {
					return err
				}
			}
		}
	}

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

func (ctx *procfsV2Ctx) parseBRWStats(nodeType string, metricType string, path string, directoryDepth int, metric *lustreProcMetric, basicLables []string) (err error) {
	_, nodeName, err := parseFileElements(path, directoryDepth)
	if err != nil {
		return err
	}

	statsFileBytes, err := ctx.fr.readFile(path)
	if err != nil {
		return err
	}
	statsFile := string(statsFileBytes[:])
	block := regexCaptureString("(?ms:^"+brwStatsMetricBlocks[metric.helpText]+".*?(\n\n|\\z))", statsFile)

	extraLabel := ""
	extraLabelValue := ""
	if metric.hasMultipleVals {
		extraLabel = "type"
		pathElements := strings.Split(path, "/")
		extraLabelValue = pathElements[len(pathElements)-3]
	}

	err = ctx.splitBRWStats(nodeType, nodeName, block, metric, basicLables, extraLabel, extraLabelValue)
	if err != nil {
		return err
	}

	return nil
}

func (ctx *procfsV2Ctx)splitBRWStats(nodeType string, nodeName string, statBlock string, metric *lustreProcMetric, lables []string, extraLable string, extraLableVal string) (err error) {
	if len(statBlock) == 0 || statBlock == "" {
		return nil
	}

	// Skip the first line of text as it doesn't contain any metrics
	for _, line := range strings.Split(statBlock, "\n")[1:] {
		if len(line) > 1 {
			fields := strings.Fields(line)
			// Lines are in the following format:
			// [size] [# read RPCs] [relative read size (%)] [cumulative read size (%)] | [# write RPCs] [relative write size (%)] [cumulative write size (%)]
			// [0]    [1]           [2]                      [3]                       [4] [5]           [6]                       [7]
			if len(fields) >= 6 {
				size, readRPCs, writeRPCs := fields[0], fields[1], fields[5]
				size = strings.Replace(size, ":", "", -1)
				size = convertToBytes(size)

				read,  err := strconv.ParseFloat(readRPCs,  64); if err != nil { return err }
				write, err := strconv.ParseFloat(writeRPCs, 64); if err != nil { return err }

				ctx.appendMetrics(metric, lables, []string{nodeType, nodeName, "read" , size}, read , extraLable, extraLableVal)
				ctx.appendMetrics(metric, lables, []string{nodeType, nodeName, "write", size}, write, extraLable, extraLableVal)
			} else if len(fields) >= 1 {
				size, rpcs := fields[0], fields[1]
				size = strings.Replace(size, ":", "", -1)
				size = convertToBytes(size)

				rpc,  err := strconv.ParseFloat(rpcs,  64); if err != nil { return err }

				ctx.appendMetrics(metric, lables, []string{nodeType, nodeName, "read" , size}, rpc , extraLable, extraLableVal)
			} else {
				continue
			}
		}
	}
	return nil
}

func (ctx *procfsV2Ctx) parseFile(nodeType string, metricType string, path string, directoryDepth int, metric *lustreProcMetric, basicLables []string) (err error) {
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
		ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName}, convertedValue, "", "")
	case stats, mdStats, encryptPagePools:
		metricList, err := ctx.parseStatsFile(path, nodeType, nodeName, metric, basicLables)
		if err != nil {
			return err
		}

		for _, item := range metricList {
			ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName}, item.value, item.extraLabel, item.extraLabelValue)
		}
	}
	return nil
}

func (ctx *procfsV2Ctx)parseStatsFile(path string, nodeType string, nodeName string, metric *lustreProcMetric, basicLables []string) (metricList []lustreStatsMetric, err error) {
	statsFileBytes, err := ctx.fr.readFile(path)
	if err != nil {
		return nil, err
	}
	statsFile := string(statsFileBytes[:])
	var statsList []lustreStatsMetric
	if metric.hasMultipleVals {
		err = ctx.getStatsOperationMetrics(statsFile, nodeType, nodeName, metric, basicLables)
	} else {
		err = ctx.getStatsIOMetrics(statsFile, nodeType, nodeName, metric, basicLables)
	}
	if err != nil {
		return nil, err
	}
	if statsList != nil {
		metricList = append(metricList, statsList...)
	}

	return metricList, nil
}

var	operationSlice = []multistatParsingStruct{
		{pattern: "open",             index: 1},
		{pattern: "close",            index: 1},
		{pattern: "getattr",          index: 1},
		{pattern: "setattr",          index: 1},
		{pattern: "getxattr",         index: 1},
		{pattern: "setxattr",         index: 1},
		{pattern: "statfs",           index: 1},
		{pattern: "seek",             index: 1},
		{pattern: "readdir",          index: 1},
		{pattern: "truncate",         index: 1},
		{pattern: "alloc_inode",      index: 1},
		{pattern: "removexattr",      index: 1},
		{pattern: "unlink",           index: 1},
		{pattern: "inode_permission", index: 1},
		{pattern: "create",           index: 1},
		{pattern: "get_info",         index: 1},
		{pattern: "set_info_async",   index: 1},
		{pattern: "connect",          index: 1},
		{pattern: "ping",             index: 1},
	}

func (ctx *procfsV2Ctx)getStatsOperationMetrics(statsFile string, nodeType string, nodeName string, metric *lustreProcMetric, basicLables []string) (err error) {

	// splits := strings.Split(statsFile, "\n")

	// for _, split := range splits {
	// 	bytesSplit := insProcfsV2.reSpace.Split(split, -1)
	// 	if len(bytesSplit) != 2 {
	// 		continue
	// 	}
	// 	val, err := strconv.ParseFloat(bytesSplit[1], 64)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName}, val, "operation", bytesSplit[0])
	// }

	// return nil

	for _, operation := range operationSlice {
		opStat := regexCaptureString(operation.pattern+" .*", statsFile)
		if len(opStat) < 1 {
			continue
		}

		bytesSplit := insProcfsV2.reSpace.Split(opStat, -1)
		result, err := strconv.ParseFloat(bytesSplit[operation.index], 64)
		if err != nil {
			return err
		}
		ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName}, result, "operation", operation.pattern)
	}
	return nil
}

var bytesMap = map[string]multistatParsingStruct{
		readSamplesHelp:       {pattern: "read_bytes .*",           index: 1},
		readMinimumHelp:       {pattern: "read_bytes .*",           index: 4},
		readMaximumHelp:       {pattern: "read_bytes .*",           index: 5},
		readTotalHelp:         {pattern: "read_bytes .*",           index: 6},
		writeSamplesHelp:      {pattern: "write_bytes .*",          index: 1},
		writeMinimumHelp:      {pattern: "write_bytes .*",          index: 4},
		writeMaximumHelp:      {pattern: "write_bytes .*",          index: 5},
		writeTotalHelp:        {pattern: "write_bytes .*",          index: 6},
		physicalPagesHelp:     {pattern: "physical pages: .*",      index: 2},
		pagesPerPoolHelp:      {pattern: "pages per pool: .*",      index: 3},
		maxPagesHelp:          {pattern: "max pages: .*",           index: 2},
		maxPoolsHelp:          {pattern: "max pools: .*",           index: 2},
		totalPagesHelp:        {pattern: "total pages: .*",         index: 2},
		totalFreeHelp:         {pattern: "total free: .*",          index: 2},
		maxPagesReachedHelp:   {pattern: "max pages reached: .*",   index: 3},
		growsHelp:             {pattern: "grows: .*",               index: 1},
		growsFailureHelp:      {pattern: "grows failure: .*",       index: 2},
		shrinksHelp:           {pattern: "shrinks: .*",             index: 1},
		cacheAccessHelp:       {pattern: "cache access: .*",        index: 2},
		cacheMissingHelp:      {pattern: "cache missing: .*",       index: 2},
		lowFreeMarkHelp:       {pattern: "low free mark: .*",       index: 3},
		maxWaitQueueDepthHelp: {pattern: "max waitqueue depth: .*", index: 3},
		outOfMemHelp:          {pattern: "out of mem: .*",          index: 3},
	}

func (ctx *procfsV2Ctx)getStatsIOMetrics(statsFile string, nodeType string, nodeName string, metric *lustreProcMetric, basicLables []string) (err error) {
	// bytesSplit is in the following format:
	// bytesString: {name} {number of samples} 'samples' [{units}] {minimum} {maximum} {sum}
	// bytesSplit:   [0]    [1]                 [2]       [3]       [4]       [5]       [6]

	pattern := bytesMap[metric.helpText].pattern
	bytesString := regexCaptureString(pattern, statsFile)
	if len(bytesString) < 1 {
		return nil
	}

	bytesSplit := insProcfsV2.reSpace.Split(bytesString, -1)
	result, err := strconv.ParseFloat(bytesSplit[bytesMap[metric.helpText].index], 64)
	if err != nil {
		return err
	}

	ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName}, result, "", "")

	return nil
}

func (ctx *procfsV2Ctx) parseJobStats(nodeType string, metricType string, path string, directoryDepth int, metric *lustreProcMetric, basicLables []string) (err error) {
	_, nodeName, err := parseFileElements(path, directoryDepth)
	if err != nil {
		return err
	}

	err = ctx.parseJobStatsText(path, nodeType, nodeName, metric, basicLables)
	if err != nil {
		return err
	}

	return nil
}

func (ctx *procfsV2Ctx)appendMetrics(metric *lustreProcMetric, basicLables []string, lableVals []string, val float64, extraLable string, extraLableVal string) {
	if extraLable != "" {
		basicLables = append(basicLables, extraLable)
		lableVals   = append(lableVals, extraLableVal)
	}
	ctx.metrics_ = append(ctx.metrics_, metric.metricFunc(basicLables, lableVals, metric.promName, metric.helpText, val) )
}

var jobStateKeys  = [22]string{
  "open",
  "close",
  "mknod",
  "link",
  "unlink",
  "mkdir",
  "rmdir",
  "rename",
  "getattr",
  "setattr",
  "getxattr",
  "setxattr",
  "statfs",
  "sync",
  "samedir_rename",
  "crossdir_rename",
  "punch",
  "destroy",
  "create",
  "get_info",
  "set_info",
  "quotactl",
}

type jobState struct {
  jobid           string
	readbytes       [4]int64
	writebytes      [4]int64
	vals            [22]int64
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
			case "open"           : js.vals[ 0], err= parsingInt64(line)
			case "close"          : js.vals[ 1], err= parsingInt64(line)
			case "mknod"          : js.vals[ 2], err= parsingInt64(line)
			case "link"           : js.vals[ 3], err= parsingInt64(line)
			case "unlink"         : js.vals[ 4], err= parsingInt64(line)
			case "mkdir"          : js.vals[ 5], err= parsingInt64(line)
			case "rmdir"          : js.vals[ 6], err= parsingInt64(line)
			case "rename"         : js.vals[ 7], err= parsingInt64(line)
			case "getattr"        : js.vals[ 8], err= parsingInt64(line)
			case "setattr"        : js.vals[ 9], err= parsingInt64(line)
			case "getxattr"       : js.vals[10], err= parsingInt64(line)
			case "setxattr"       : js.vals[11], err= parsingInt64(line)
			case "statfs"         : js.vals[12], err= parsingInt64(line)
			case "sync"           : js.vals[13], err= parsingInt64(line)
			case "samedir_rename" : js.vals[14], err= parsingInt64(line)
			case "crossdir_rename": js.vals[15], err= parsingInt64(line)
			case "punch"          : js.vals[16], err= parsingInt64(line)
			case "destroy"        : js.vals[17], err= parsingInt64(line)
			case "create"         : js.vals[18], err= parsingInt64(line)
			case "get_info"       : js.vals[19], err= parsingInt64(line)
			case "set_info"       : js.vals[20], err= parsingInt64(line)
			case "quotactl"       : js.vals[21], err= parsingInt64(line)
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

func parsingInt64(input string)(int64, error){
	numStrs := insProcfsV2.reNum.FindAllString(input, 1)
	if len(numStrs) < 1 {
		return 0, fmt.Errorf("can not find any num strings")
	}

	num, err := strconv.ParseInt(strings.TrimSpace(numStrs[0]), 10, 64)
	if err != nil {
		return 0, err
	}

	return num, nil
}

func (ctx *procfsV2Ctx)parseJobStatsText(path string, nodeType string, nodeName string, metric *lustreProcMetric, basicLables []string) (err error){

	jobsStats, ok := ctx.filesJobStats[path]
	if !ok {
		jobsStats = sPool.newJobStates()
		jobStatsBytes, err := ctx.fr.readFile(path)
		if err != nil {
			return err
		}
		jobStatsContent := string(jobStatsBytes[:])
		splits := strings.Split(jobStatsContent, "- ")
		if len(splits) <= 1{
			return nil
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

	if metric.hasMultipleVals {
		ctx.getJobStatsOperationMetrics(*jobsStats, nodeType, nodeName, metric, basicLables)
	} else {
		ctx.getJobStatsIOMetrics(*jobsStats, nodeType, nodeName, metric, basicLables)
	}

	return nil
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

// deprecated, not used now
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

func (ctx *procfsV2Ctx)getJobStatsOperationMetrics(jobsStats []jobState, nodeType string, nodeName string, metric *lustreProcMetric, basicLables []string) (err error) {

	basicLables = append(basicLables, "operation")

	for i := range jobsStats {
		js  := &jobsStats[i]
		cnt := len(jobStateKeys)
		for j := 0; j < cnt; j++ {
			if js.vals[j] < 0 {
				continue
			}
			ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName, js.jobid, jobStateKeys[j]}, float64(js.vals[j]), "", "")
		}
	}

	return
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

func (ctx *procfsV2Ctx)getJobStatsIOMetrics(jobsStats []jobState, nodeType string, nodeName string, metric *lustreProcMetric, basicLables []string) (err error){
	// opMap matches the given helpText value with the placement of the numeric fields within each metric line.
	// For example, the number of samples is the first number in the line and has a helpText of readSamplesHelp,
	// hence the 'index' value of 0. 'pattern' is the regex capture pattern for the desired line.
	opMap := jobStatsMultistatParsingStruct
	// If the metric isn't located in the map, don't try to parse a value for it.
	if _, exists := opMap[metric.helpText]; !exists {
		return nil
	}

	operation := opMap[metric.helpText]
	for _, js := range jobsStats {
		if operation.pattern == "read_bytes" {
			ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName, js.jobid}, float64(js.readbytes[operation.index]), "", "")
			continue
		}
		if operation.pattern == "write_bytes" {
			ctx.appendMetrics(metric, basicLables, []string{nodeType, nodeName, js.jobid}, float64(js.writebytes[operation.index]), "", "")
		}
	}

	return 
}

func init(){
	insProcfsV2.reNum   = regexp.MustCompile(`[0-9]*\.[0-9]+|[0-9]+`)
	insProcfsV2.reSpace = regexp.MustCompile(` +`)
	jobStateInitVal.__init()
}