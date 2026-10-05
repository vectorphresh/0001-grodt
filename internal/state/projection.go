package state

import "encoding/json"

// ModelJSON projects current knowledge and actionable tasks. JSON and Journal
// remain the full audit interfaces; completed work is not model context.
// Partition values are module-owned current world state, not event archives.
func (s *Store) ModelJSON() json.RawMessage {
	type taskView struct {
		Plan          *Plan             `json:"plan,omitempty"`
		EvidenceState map[string]string `json:"evidence_state,omitempty"`
		ID            string            `json:"id"`
		ParentID      string            `json:"parent_id,omitempty"`
		Objective     string            `json:"objective"`
		Status        string            `json:"status"`
		Cycles        uint64            `json:"cycles"`
		Server        string            `json:"server,omitempty"`
		Tool          string            `json:"tool,omitempty"`
		Partition     string            `json:"partition,omitempty"`
		WorkKind      string            `json:"work_kind,omitempty"`
	}
	view := struct {
		Intrinsic Intrinsic `json:"intrinsic"`
		Tasks     struct {
			Records map[string]taskView `json:"records"`
			Stack   []string            `json:"stack"`
		} `json:"tasks"`
		Knowledge           map[string]Partition        `json:"knowledge"`
		EstablishedProgress map[string][]ProgressRecord `json:"established_progress"`
		ActiveFocus         map[string][]ProgressRecord `json:"active_focus"`
	}{Intrinsic: s.snapshot.Intrinsic, Knowledge: map[string]Partition{}, EstablishedProgress: map[string][]ProgressRecord{}, ActiveFocus: map[string][]ProgressRecord{}}
	view.Tasks.Records = map[string]taskView{}
	view.Tasks.Stack = s.snapshot.Tasks.Stack
	lineage := map[string]bool{}
	for _, id := range s.snapshot.Tasks.Stack {
		lineage[id] = true
	}
	for id, t := range s.snapshot.Tasks.Records {
		if t.Status == "running" || t.Status == "pending" || t.Status == "waiting" || (t.Plan != nil && lineage[t.ParentID]) {
			v := taskView{ID: t.ID, ParentID: t.ParentID, Objective: t.Objective, Status: t.Status, Cycles: t.Cycles, Plan: t.Plan}
			if t.Plan != nil {
				v.EvidenceState = map[string]string{}
				refs := append([]EvidenceReference(nil), t.Plan.Evidence...)
				for _, step := range t.Plan.Steps {
					refs = append(refs, step.Evidence...)
				}
				for _, ref := range refs {
					status := "current"
					partition, ok := s.snapshot.Knowledge[ref.Partition]
					if !ok {
						status = "missing"
					} else if partition.Metadata.Stale {
						status = "stale"
					} else if partition.Metadata.Version != ref.Version {
						status = "superseded"
					}
					v.EvidenceState[string(encode(ref))] = status
				}
			}
			if t.AgentWork != nil {
				v.Server, v.Tool = t.AgentWork.Server, t.AgentWork.Tool
			}
			if t.Work != nil {
				v.Partition, v.WorkKind = t.Work.Partition, t.Work.Request.Kind
			}
			view.Tasks.Records[id] = v
		}
	}
	for name, p := range s.snapshot.Knowledge {
		view.Knowledge[name] = Partition{Metadata: p.Metadata, Value: p.Value}
		for _, record := range p.Progress {
			if _, relevant := view.Tasks.Records[record.TaskID]; !relevant {
				continue
			}
			if p.Metadata.Stale || record.KnowledgeVersion != p.Metadata.Version {
				record.Status = "invalidated"
			}
			if record.Kind == "focus" {
				view.ActiveFocus[name] = append(view.ActiveFocus[name], record)
			} else {
				view.EstablishedProgress[name] = append(view.EstablishedProgress[name], record)
			}
		}
	}
	return encode(view)
}
