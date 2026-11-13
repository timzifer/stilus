package gpu

// SetStoreLimit sets the bound of the texture store for a test and
// returns a function restoring it.
func SetStoreLimit(n int) func() {
	old := storeLimit
	storeLimit = n
	return func() { storeLimit = old }
}

// MeshBuilds returns how many mesh records t and its recorders made.
func MeshBuilds(t *Target) int {
	t.meshes.mu.Lock()
	defer t.meshes.mu.Unlock()
	return t.meshes.builds
}

// SetMeshCache turns the mesh record cache on or off and returns a
// function restoring it.
func SetMeshCache(on bool) func() {
	old := meshCacheOff
	meshCacheOff = !on
	return func() { meshCacheOff = old }
}
