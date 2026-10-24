// Recipes passed to the shell as a file (the F attribute).

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	recipeFiles   = map[string]bool{} // files and directories not yet removed
	cleanupOnce   sync.Once
)

// recipeLocation is where F recipe files are written, as chosen by the
// MKRECIPEDIR variable.
type recipeLocation struct {
	dir     string // the directory to write into
	private bool   // make a private directory inside dir for each recipe
}

// recipeLocationFor interprets MKRECIPEDIR:
//
//	auto (or unset)  xdg if $XDG_RUNTIME_DIR is set, otherwise tmp
//	xdg              a private directory in $XDG_RUNTIME_DIR
//	tmp              a private directory in $TMPDIR
//	a path           a hidden file directly in that directory (a path is
//	                 ".", "..", or anything containing a /)
//
// Private directories keep other users from planting files next to the
// recipe that an interpreter would load (Python, for one, searches the
// script's directory for imports). A path is for when the script's directory
// matters, e.g. so that imports resolve there; its security is then up to
// the user.
func recipeLocationFor(vars map[string][]string) (recipeLocation, error) {
	setting := strings.Join(vars["MKRECIPEDIR"], " ")
	xdg := strings.Join(vars["XDG_RUNTIME_DIR"], " ")
	tmp := strings.Join(vars["TMPDIR"], " ")
	if tmp == "" {
		tmp = os.TempDir()
	}
	switch {
	case setting == "" || setting == "auto":
		if xdg != "" {
			return recipeLocation{xdg, true}, nil
		}
		return recipeLocation{tmp, true}, nil
	case setting == "xdg":
		if xdg == "" {
			return recipeLocation{}, fmt.Errorf("MKRECIPEDIR is xdg, but $XDG_RUNTIME_DIR is not set")
		}
		return recipeLocation{xdg, true}, nil
	case setting == "tmp":
		return recipeLocation{tmp, true}, nil
	case setting == "." || setting == ".." ||
		strings.ContainsRune(setting, '/') || strings.ContainsRune(setting, filepath.Separator):
		return recipeLocation{setting, false}, nil
	}
	return recipeLocation{}, fmt.Errorf("MKRECIPEDIR: unknown value %q (want auto, xdg, tmp, or a path such as . or ./dir)", setting)
}

// writeRecipeFile writes a recipe to a new file at loc. It returns the path
// of the script, and the path to give removeRecipeFile afterwards (the
// private directory, or the script itself). Names include the target, so
// the shell's error messages say which recipe they come from.
func writeRecipeFile(loc recipeLocation, target, recipe string) (script, remove string, err error) {
	if !keepTempFiles {
		cleanupOnce.Do(removeRecipeFilesOnSignal)
	}
	name := fileNamePart(target)

	if loc.private {
		dir, err := os.MkdirTemp(loc.dir, "mk-"+name+"-*") // mode 0700
		if err != nil {
			return "", "", err
		}
		trackRecipeFile(dir)
		script = filepath.Join(dir, "recipe")
		if err := os.WriteFile(script, []byte(recipe), 0o600); err != nil {
			removeRecipeFile(dir)
			return "", "", err
		}
		remove = dir
	} else {
		f, err := os.CreateTemp(loc.dir, ".mk-recipe-"+name+"-*")
		if err != nil {
			return "", "", err
		}
		trackRecipeFile(f.Name())
		_, err = f.WriteString(recipe)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			removeRecipeFile(f.Name())
			return "", "", err
		}
		script, remove = f.Name(), f.Name()
	}

	if keepTempFiles {
		mkMsgMutex.Lock()
		fmt.Fprintf(os.Stderr, "mk: recipe for '%s' kept in %s\n", target, script)
		mkMsgMutex.Unlock()
	}
	return script, remove, nil
}

func trackRecipeFile(path string) {
	recipeFilesMu.Lock()
	recipeFiles[path] = true
	recipeFilesMu.Unlock()
}

// removeRecipeFile removes a file or private directory made by
// writeRecipeFile, unless -keep-temp-files was given.
func removeRecipeFile(path string) {
	if keepTempFiles {
		return
	}
	recipeFilesMu.Lock()
	defer recipeFilesMu.Unlock()
	os.RemoveAll(path)
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
			os.RemoveAll(path)
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
