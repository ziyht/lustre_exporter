package sources

import "sync"

type pool struct{

	jobStats  sync.Pool
	jobStatss sync.Pool
}

var sPool = &pool{
	jobStats : sync.Pool{ New: newJobState },
	jobStatss: sync.Pool{ New: newJobStates },
}

func (p *pool)newJobState() *jobState{
	return p.jobStats.Get().(*jobState)
}

func (p *pool)recycleJobState(d *jobState) {
	p.jobStats.Put(d)
}

func (p *pool)newJobStates() *[]jobState{
	out := (p.jobStatss.Get().(*[]jobState))
	*out = (*out)[0:0]
	return out
}

func (p *pool)recycleJobStates(d *[]jobState) {
	p.jobStatss.Put(d)
}