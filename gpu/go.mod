module github.com/timzifer/stilus/gpu

go 1.25.0

replace github.com/timzifer/stilus => ../

require (
	github.com/gogpu/gputypes v0.6.0
	github.com/gogpu/wgpu v0.32.1
	github.com/timzifer/stilus v0.0.0-00010101000000-000000000000
)

require (
	github.com/go-webgpu/goffi v0.6.3 // indirect
	github.com/go-webgpu/webgpu v0.5.5 // indirect
	github.com/gogpu/gpucontext v0.29.0 // indirect
	github.com/gogpu/naga v0.19.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
