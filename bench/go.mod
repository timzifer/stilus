module github.com/timzifer/stilus/bench

go 1.26.0

replace github.com/timzifer/stilus => ../

require (
	github.com/gogpu/gg v0.52.5
	github.com/timzifer/figure v0.14.0
	github.com/timzifer/stilus v0.0.0-00010101000000-000000000000
	golang.org/x/image v0.46.0
)

require (
	github.com/gogpu/gpucontext v0.28.0 // indirect
	github.com/gogpu/gputypes v0.5.2 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)
