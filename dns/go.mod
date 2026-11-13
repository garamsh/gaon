module github.com/garamsh/gaon/dns

go 1.26.0

require (
	github.com/garamsh/gaon/lib/api v0.0.0-00010101000000-000000000000
	github.com/miekg/dns v1.1.73
	github.com/stretchr/testify v1.12.1
)

require (
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/garamsh/gaon/lib/api => ../lib/api
