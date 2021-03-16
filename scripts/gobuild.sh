set -x
export PATH="$PATH:/usr/local/go/bin"
cd ..
export GOPATH=export GOPATH=$PWD
export PATH=$PATH:$GOPATH/bin
export GOROOT=/usr/local/go
export PATH=$PATH:$GOROOT/bin
cd ../src/ && go build -o nixy
DATE_WITH_TIME=`date "+%Y%m%d-%H%M%S"` #add %3N as we want millisecond too
mv /usr/bin/nixy /usr/bin/nixy-$DATE_WITH_TIME
ls -lath /usr/bin/nixy-$DATE_WITH_TIME
mv nixy /usr/bin/nixy
/usr/bin/nixy -v
cp nixy.toml /etc/nixy
