package main

// jobStats counts what a backup moved. Each file fills its own value on the
// stream goroutine that processes it (plain adds, no atomics); main sums the
// values as results arrive on the single result channel.
type jobStats struct {
	bytesRead      int64 // chunk bytes read from disk for files the server wanted
	bytesSent      int64 // chunk bytes the server did not already have
	filesSent      int64 // files whose chunks were transferred
	filesUnchanged int64 // files the server already had
	bytesUnchanged int64 // size of those files; never read
}

func (s *jobStats) add(o jobStats) {
	s.bytesRead += o.bytesRead
	s.bytesSent += o.bytesSent
	s.filesSent += o.filesSent
	s.filesUnchanged += o.filesUnchanged
	s.bytesUnchanged += o.bytesUnchanged
}

// dedupRatio is bytes read over bytes sent: 1 means nothing was already
// stored, higher means more was deduplicated. With nothing sent it is 0 when
// nothing was read either, otherwise there is no finite ratio and 0 is
// returned too; callers should read bytesSent == 0 as "fully deduplicated".
func (s jobStats) dedupRatio() float64 {
	if s.bytesSent == 0 {
		return 0
	}
	return float64(s.bytesRead) / float64(s.bytesSent)
}
