module github.com/garamsh/gaon/gaon-cli

go 1.24

require (
	github.com/garamsh/gaon/lib/api v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.12.1
	go.yaml.in/yaml/v3 v3.0.6
)

replace github.com/garamsh/gaon/lib/api => ../lib/api
