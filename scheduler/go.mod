module github.com/garamsh/gaon/scheduler

go 1.24

require (
	github.com/garamsh/gaon/lib/api v0.0.0
	github.com/stretchr/testify v1.12.1
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect

replace github.com/garamsh/gaon/lib/api => ../lib/api
