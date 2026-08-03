.PHONY: demo test compile

demo:
	go run ./cmd/demo

test:
	go test ./...

# Only needed after editing the contract (run npm i solc@0.8.24 first)
compile:
	node contracts/compile.js
