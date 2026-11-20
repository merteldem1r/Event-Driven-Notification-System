// Keeps web/ (and the Go files some npm packages ship in node_modules) out of the
// service's module, so go vet, go test and golangci-lint ignore it.
module web

go 1.26
