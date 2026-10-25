# Example mkfile: open it in your editor to check the highlighting.
CC = cc
CFLAGS = -O2 -Wall
SRC = main.c util.c
OBJ = ${SRC:%.c=%.o}
TOKEN=U=not-exported
PREFIX ?= /usr/local
FILES = `ls *.c`
RCFILES = `{ls *.c}
SC = VQ
<config.mk
<|./gen-rules.sh $CC

all:V: prog

prog: $OBJ
    $CC -o $target $prereq

%.o: %.c
    $CC $CFLAGS -c $stem.c

quiet:$SC:
    echo "only the result is shown"

clean:VE:
    dir=build
    rm -rf $$dir *.o   # remove build output

deps: a \
    b c

report:VSpython3:
    print("hello from python")

out%: Dockerfile
    rm -rf $target
    docker build --output $target -f $prereq1 .
