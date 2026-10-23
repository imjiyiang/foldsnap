//go:build !darwin

package backup

func cloneStagedFile(from, to string) bool { return false }
