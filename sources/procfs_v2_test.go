package sources

import (
	"fmt"
	"testing"

	"github.com/alecthomas/assert"
)

func TestParsingJobStats(t *testing.T){

  jobstr := `job_id:          kworker/14:1.0
	snapshot_time:   1652255649
	read_bytes:      { samples:           0, unit: bytes, min:       0, max:       0, sum:               0 }
	write_bytes:     { samples:           0, unit: bytes, min:       0, max:       0, sum:               0 }
	getattr:         { samples:           0, unit:  reqs }
	setattr:         { samples:           0, unit:  reqs }
	punch:           { samples:           0, unit:  reqs }
	sync:            { samples:           0, unit:  reqs }
	destroy:         { samples:           0, unit:  reqs }
	create:          { samples:           0, unit:  reqs }
	statfs:          { samples:           0, unit:  reqs }
	get_info:        { samples:           0, unit:  reqs }
	set_info:        { samples:         286, unit:  reqs }
	quotactl:        { samples:           0, unit:  reqs }`

	jobid, data, _ := insProcfsV2.newCtx(nil).parsingJobStats(jobstr)
	fmt.Printf("%s: %v\n", jobid, data)
}

func TestParsingJobStatsV2(t *testing.T){
  jobstr := `job_id:          kworker/14:1.0
	snapshot_time:   1652255649
	read_bytes:      { samples:           0, unit: bytes, min:       0, max:       0, sum:               0 }
	write_bytes:     { samples:           0, unit: bytes, min:       0, max:       0, sum:               0 }
	getattr:         { samples:           0, unit:  reqs }
	setattr:         { samples:           0, unit:  reqs }
	punch:           { samples:           0, unit:  reqs }
	sync:            { samples:           0, unit:  reqs }
	destroy:         { samples:           0, unit:  reqs }
	create:          { samples:           0, unit:  reqs }
	statfs:          { samples:           0, unit:  reqs }
	get_info:        { samples:           0, unit:  reqs }
	set_info:        { samples:         286, unit:  reqs }
	quotactl:        { samples:           0, unit:  reqs }`

	js := sPool.newJobState()
	err := js.parsingFromText(jobstr)
	assert.Nil(t, err)
	assert.Equal(t, int64(0), js.readbytes[0])
	assert.Equal(t, int64(0), js.readbytes[1])
	assert.Equal(t, int64(0), js.readbytes[2])
	assert.Equal(t, int64(0), js.readbytes[3])
	assert.Equal(t, int64(0), js.writebytes[0])
	assert.Equal(t, int64(0), js.writebytes[1])
	assert.Equal(t, int64(0), js.writebytes[2])
	assert.Equal(t, int64(0), js.writebytes[3])
	assert.Equal(t, int64(0), js.getattr)
	assert.Equal(t, int64(0), js.setattr)
	assert.Equal(t, int64(0), js.statfs)
	assert.Equal(t, int64(0), js.sync)
	assert.Equal(t, int64(0), js.punch)
	assert.Equal(t, int64(0), js.destroy)
	assert.Equal(t, int64(0), js.create)
	assert.Equal(t, int64(0), js.get_info)
	assert.Equal(t, int64(286), js.set_info)
	assert.Equal(t, int64(0), js.quotactl)

	assert.Equal(t, int64(-1), js.open)
	assert.Equal(t, int64(-1), js.close)
	assert.Equal(t, int64(-1), js.mknod)
	assert.Equal(t, int64(-1), js.link)
	assert.Equal(t, int64(-1), js.unlink)
	assert.Equal(t, int64(-1), js.mkdir)
	assert.Equal(t, int64(-1), js.rename)
	assert.Equal(t, int64(-1), js.samedir_rename)
	assert.Equal(t, int64(-1), js.crossdir_rename)

	sPool.recycleJobState(js)
}