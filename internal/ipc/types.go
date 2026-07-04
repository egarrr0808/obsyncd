package ipc

type StatusArgs struct{}

type StatusReply struct {
	FolderID        string
	FolderState     string
	FolderStateTime string
	OracleName      string
	OracleDeviceID  string
	OracleConnected bool
	ManualConflicts []string
	Pending         []PendingConflict
}

type PendingConflict struct {
	Canonical string
	Staged    string
}

type RescanArgs struct {
	Paths []string
}

type RescanReply struct {
	FolderID string
	Paths    []string
	OK       bool
}

type ResolveArgs struct {
	Path   string
	Action string
}

type ResolveReply struct {
	Path string
	OK   bool
}
