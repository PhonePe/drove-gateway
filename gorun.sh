export PATH="$PATH:/usr/local/go/bin"
export GOPATH=export GOPATH=$PWD
export PATH=$PATH:$GOPATH/bin
export GOROOT=/usr/local/go
export PATH=$PATH:$GOROOT/bin
cd src/ && go run *.go
