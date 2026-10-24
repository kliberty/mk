// Recipes passed to the shell as a file (the F attribute).

package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// keepTempFiles (the -keep-temp-files flag) keeps every recipe file
	// for debugging, instead of removing it.
	keepTempFiles bool

	recipeFilesMu sync.Mutex
	recipeFiles   = map[string]bool{} // recipe files not yet removed
	cleanupOnce   sync.Once
)

// writeRecipeFile writes a recipe to a new temporary file (in $TMPDIR) and
// returns its path. The name includes the target, so the shell's error
// messages say which recipe they come from. The caller must remove the file
// with removeRecipeFile.
func writeRecipeFile(target, recipe string) (string, error) {
	if !keepTempFiles {
		cleanupOnce.Do(removeRecipeFilesOnSignal)
	}

	f, err := os.CreateTemp("", "mk-"+fileNamePart(target)+"-*")
	if err != nil {
		return "", err
	}
	recipeFilesMu.Lock()
	recipeFiles[f.Name()] = true
	recipeFilesMu.Unlock()

	_, err = f.WriteString(recipe)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeRecipeFile(f.Name())
		return "", err
	}
	if keepTempFiles {
		mkMsgMutex.Lock()
		fmt.Fprintf(os.Stderr, "mk: recipe for '%s' kept in %s\n", target, f.Name())
		mkMsgMutex.Unlock()
	}
	return f.Name(), nil
}

// removeRecipeFile removes a file made by writeRecipeFile, unless
// -keep-temp-files was given.
func removeRecipeFile(path string) {
	if keepTempFiles {
		return
	}
	recipeFilesMu.Lock()
	defer recipeFilesMu.Unlock()
	os.Remove(path)
	delete(recipeFiles, path)
}

// removeRecipeFilesOnSignal removes any remaining recipe files if mk is
// interrupted or terminated, then lets the signal end mk as it otherwise
// would have.
func removeRecipeFilesOnSignal() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-c
		recipeFilesMu.Lock() // held until exit, so no new files appear
		for path := range recipeFiles {
			os.Remove(path)
		}
		// Re-raise the signal so mk ends the way it would have without
		// this handler. Delivery is asynchronous, so allow it a moment
		// before falling back to an ordinary exit.
		signal.Reset(sig)
		if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(sig) == nil {
			time.Sleep(time.Second)
		}
		os.Exit(1)
	}()
}

// fileNamePart turns a target name into something safe to use in a file
// name: other characters become '_', and long names are shortened.
func fileNamePart(name string) string {
	const maxLen = 40
	var b strings.Builder
	for _, c := range name {
		if b.Len() >= maxLen {
			break
		}
		if c < 128 && (c == '.' || c == '-' || c == '_' ||
			('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')) {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
