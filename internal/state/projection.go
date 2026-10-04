package state

import "encoding/json"

// ModelJSON projects current knowledge and actionable tasks. JSON and Journal
// remain the full audit interfaces; completed work is not model context.
// Partition values are module-owned current world state, not event archives.
func (s *Store) ModelJSON() json.RawMessage {
	type taskView struct {
		ID        string `json:"id"`
		ParentID  string `json:"parent_id,omitempty"`
		Objective string `json:"objective"`
		Status    string `json:"status"`
		Cycles    uint64 `json:"cycles"`
		Server    string `json:"server,omitempty"`
		Tool      string `json:"tool,omitempty"`
		Partition string `json:"partition,omitempty"`
		WorkKind  string `json:"work_kind,omitempty"`
	}
	view := struct {
		Intrinsic Intrinsic `json:"intrinsic"`
		Tasks     struct {
			Records map[string]taskView `json:"records"`
			Stack   []string            `json:"stack"`
		} `json:"tasks"`
		Knowledge map[string]Partition `json:"knowledge"`
	}{Intrinsic: s.snapshot.Intrinsic, Knowledge: s.snapshot.Knowledge}
	view.Tasks.Records = map[string]taskView{}
	view.Tasks.Stack = s.snapshot.Tasks.Stack
	for id, t := range s.snapshot.Tasks.Records {
		if t.Status == "running" || t.Status == "pending" || t.Status == "waiting" {
			v := taskView{ID: t.ID, ParentID: t.ParentID, Objective: t.Objective, Status: t.Status, Cycles: t.Cycles}
			if t.AgentWork != nil {
				v.Server, v.Tool = t.AgentWork.Server, t.AgentWork.Tool
			}
			if t.Work != nil {
				v.Partition, v.WorkKind = t.Work.Partition, t.Work.Request.Kind
			}
			view.Tasks.Records[id] = v
		}
	}
	return encode(view)
}
