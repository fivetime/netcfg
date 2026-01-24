/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package version

var (
	Version   = "0.2.0"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

func Full() string {
	return Version + " (commit: " + GitCommit + ", built: " + BuildDate + ")"
}
