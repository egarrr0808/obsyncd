package ipc

type StatusArgs struct{}

type StatusReply struct {
	FolderID        string
	FolderState     string
	FolderStateTime string
	OracleName      string
	OracleDeviceID  string
	OracleConnected bool
}

type RescanArgs struct {
	Paths []string
}

type RescanReply struct {
	FolderID string
	Paths    []string
	OK       bool
}
