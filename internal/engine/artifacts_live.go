package engine

// ArtifactOptions returns the options artifacts are kept with.
func (e *Engine) ArtifactOptions() ArtifactOptions {
	o := e.arts
	l := e.live().arts
	o.MaxFileBytes, o.MaxRunBytes, o.MaxProjectBytes, o.DefaultRetain, o.MaxRetain = e.maxFileBytes(), l.MaxRunBytes, l.MaxProjectBytes, l.DefaultRetain, l.MaxRetain
	return o
}

// maxFileBytes is the largest file an artifact may hold: the limit, or the
// largest object the store keeps, if that is less.
func (e *Engine) maxFileBytes() int64 {
	n := e.live().arts.MaxFileBytes
	if e.objectCap > 0 && n > e.objectCap {
		return e.objectCap
	}
	return n
}
