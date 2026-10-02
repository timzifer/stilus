package stilus

// SetNoCull turns the stroker's culling against the clip off, for tests
// comparing Stroke with FillShape.
func (c *Canvas) SetNoCull(v bool) { c.s.noCull = v }
