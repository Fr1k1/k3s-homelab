package main

import (
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// ctrlZapLogger builds the structured (JSON) logger controller-runtime and
// every reconcile call use — controller-runtime is built on the logr
// interface, not a concrete logging library, so it can be backed by
// whichever implementation a project prefers; this project uses zap, the
// same choice kubebuilder scaffolds by default. Kept in its own tiny file
// so main.go's job stays "wire the manager together," not "configure
// logging."
func ctrlZapLogger() logr.Logger {
	return zap.New(zap.UseDevMode(false))
}
