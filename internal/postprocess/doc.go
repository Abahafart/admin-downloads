// Package postprocess runs the steps that follow a finished download
// (PLAN.md phase 7): checksum verification and automatic archive
// extraction.
//
// # Wiring
//
// internal/app builds one Processor from the [postprocess] section of
// config.toml and hands it to the engine:
//
//	pp := postprocess.New(cfg.PostProcess, log.With("component", "postprocess"))
//	pp.RegisterVerifier(hosters.AlgoMEGAMAC, megaMACVerifier) // from the MEGA plugin
//
// The engine calls Process once per completed download, after the .part
// file has been renamed to its final name, with the download in state
// "verifying"/"postprocessing":
//
//	err := pp.Process(ctx, postprocess.Item{
//		Path:       finalPath,
//		Checksum:   spec.Checksum, // or FileInfo.Checksum; nil = nothing to verify
//		PackageDir: pkg.Dir,       // extraction destination
//		Passwords:  pkg.Passwords, // per-package passwords, tried first
//		Progress:   func(p postprocess.Progress) { /* emit an event */ },
//	})
//
// Use Run instead of Process to get the Result (files written, entries
// rejected, deleted volumes, whether a multi-volume set is still waiting
// for volumes). Errors wrap the sentinels below so the engine can show a
// clear message: ErrChecksumMismatch (the download is bad: retry or
// restart it), ErrWrongPassword / ErrPasswordRequired (ask the user for a
// password and call Process again), ErrCorrupt and ErrUnsupported.
//
// Multi-volume archives (.partN.rar, .rar+.rNN, .7z.001, .zip.001) can be
// handed in any order, one volume per call: Process finds the first
// volume, checks that the whole set is on disk, and returns a Result with
// Waiting=true (and a nil error) until it is. Each set is extracted once
// per process.
//
// # Configuration ([postprocess])
//
//   - verify_checksums: verify Item.Checksum (Options.Verify overrides).
//   - extract: extract zip / 7z / rar archives (Options.Extract overrides).
//   - passwords: archive passwords tried after Item.Passwords.
//   - delete_archives: delete every volume after a successful extraction
//     (Options.DeleteArchives overrides).
//
// # Security
//
// Entry names are untrusted. Names that are absolute, carry a drive letter
// or contain a ".." component are rejected (listed in Result.Rejected,
// never written); every other name goes through fsutil.SafeJoin. Symlinks,
// hard links and devices are never created. Extraction happens in a
// staging directory inside the destination and is moved into place only
// when the whole archive was extracted, so a wrong password or a
// cancellation never leaves half an archive behind. Passwords are never
// logged or included in errors.
package postprocess
