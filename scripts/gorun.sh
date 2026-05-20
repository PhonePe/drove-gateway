export PATH="$PATH:/usr/local/go/bin"
export GOPATH=$PWD
export PATH=$PATH:$GOPATH/bin
export GOROOT=/usr/local/go
export PATH=$PATH:$GOROOT/bin
# Go module and main package live in src/
cd "$(dirname "$0")/../src" && go run .
