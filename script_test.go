package main

import (
	"context"
	"os"
	"testing"

	"rsc.io/script"
	"rsc.io/script/scripttest"
)

func TestScript(t *testing.T) {
	t.Parallel()

	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	engine := &script.Engine{
		Cmds:  scripttest.DefaultCmds(),
		Conds: scripttest.DefaultConds(),
		Quiet: !testing.Verbose(),
	}
	// Register the test binary itself as the "mk" command.
	// TEST_MAIN=mk causes TestMain to dispatch to main(), so the
	// test binary acts as mk without a separate build step.
	engine.Cmds["mk"] = script.Program(testBin, nil, 0)

	env := os.Environ()
	// $MK lets a script run mk through sh, e.g. to redirect its stdin.
	env = append(env, "TEST_MAIN=mk", "MK="+testBin)

	scripttest.Test(t, context.Background(), engine, env, "testdata/*.txt")
}
