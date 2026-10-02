package taodata

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/filelock"
)

// Raw fields retain settings unknown to legacy callers, including invalid values
// that the settings service must be able to inspect and repair.
func (d *RepoRunDefaults) UnmarshalJSON(data []byte) error {
	var raw configtypes.SettingsValues
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = RepoRunDefaults{Extra: raw}
	var pull bool
	if value, ok := raw["pull_request"]; ok && string(value) != "null" && json.Unmarshal(value, &pull) == nil {
		d.PullRequest = &pull
		delete(d.Extra, "pull_request")
	}
	var models RepoModelDefaults
	if value, ok := raw["models"]; ok && string(value) != "null" && json.Unmarshal(value, &models) == nil {
		d.Models = &models
	}
	var reviewAgent string
	if value, ok := raw["review_agent"]; ok && json.Unmarshal(value, &reviewAgent) == nil && reviewAgent != "" {
		d.ReviewAgent = reviewAgent
		delete(d.Extra, "review_agent")
	}
	for key, field := range map[string]**int{"max_rework_attempts": &d.MaxReworkAttempts, "rework_escalation_from_attempt": &d.ReworkEscalationFromAttempt} {
		var n int
		if value, ok := raw[key]; ok && string(value) != "null" && json.Unmarshal(value, &n) == nil {
			*field = &n
			delete(d.Extra, key)
		}
	}
	return nil
}

func (d RepoRunDefaults) MarshalJSON() ([]byte, error) {
	if len(d.Extra) == 0 {
		type plain RepoRunDefaults
		return json.Marshal(plain(d))
	}
	raw := d.Extra.Clone()
	if raw == nil {
		raw = configtypes.SettingsValues{}
	}
	if d.PullRequest != nil {
		data, err := json.Marshal(d.PullRequest)
		if err != nil {
			return nil, err
		}
		raw["pull_request"] = data
	}
	if d.ReviewAgent != "" {
		data, err := json.Marshal(d.ReviewAgent)
		if err != nil {
			return nil, err
		}
		raw["review_agent"] = data
	}
	for key, field := range map[string]*int{"max_rework_attempts": d.MaxReworkAttempts, "rework_escalation_from_attempt": d.ReworkEscalationFromAttempt} {
		if field == nil {
			continue
		}
		data, err := json.Marshal(*field)
		if err != nil {
			return nil, err
		}
		raw[key] = data
	}
	var original RepoModelDefaults
	unchangedModels := d.Models != nil && json.Unmarshal(raw["models"], &original) == nil && original == *d.Models
	if d.Models != nil && !unchangedModels {
		models := map[string]json.RawMessage{}
		_ = json.Unmarshal(raw["models"], &models)
		if models == nil {
			models = map[string]json.RawMessage{}
		}
		// Replace only known roles; future model fields remain untouched.
		for _, key := range modelFields {
			delete(models, key)
		}
		data, err := json.Marshal(d.Models)
		if err != nil {
			return nil, err
		}
		var known map[string]json.RawMessage
		if err := json.Unmarshal(data, &known); err != nil {
			return nil, err
		}
		for k, v := range known {
			models[k] = v
		}
		data, err = json.Marshal(models)
		if err != nil {
			return nil, err
		}
		raw["models"] = data
	}
	return json.Marshal(raw)
}

var modelFields = []string{"model", "run_model", "review_model", "merge_review_model", "resolver_model", "rework_escalation_model"}

func (d *RepoRunDefaults) clearModels() {
	var raw map[string]json.RawMessage
	if json.Unmarshal(d.Extra["models"], &raw) == nil && raw != nil {
		for _, key := range modelFields {
			delete(raw, key)
		}
		if len(raw) > 0 {
			data, _ := json.Marshal(raw)
			d.Extra["models"] = data
			return
		}
	}
	delete(d.Extra, "models")
}

func (d RepoRunDefaults) empty() bool {
	return d.ReviewAgent == "" && d.PullRequest == nil && d.Models == nil && d.MaxReworkAttempts == nil && d.ReworkEscalationFromAttempt == nil && len(d.Extra) == 0
}

func (r Registry) lockRepo(ctx context.Context, id string) (func(), error) {
	dir := filepath.Join(r.DataHome, "repos", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "repo.json.lock"), os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // registry-owned path derived from canonical repository ID
	if err != nil {
		return nil, err
	}
	if err := filelock.LockPoll(ctx, f, 10*time.Millisecond); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
}

func (d RepoRunDefaults) clone() RepoRunDefaults {
	d.Extra = d.Extra.Clone()
	if d.PullRequest != nil {
		v := *d.PullRequest
		d.PullRequest = &v
	}
	if d.Models != nil {
		v := *d.Models
		d.Models = &v
	}
	if d.MaxReworkAttempts != nil {
		v := *d.MaxReworkAttempts
		d.MaxReworkAttempts = &v
	}
	if d.ReworkEscalationFromAttempt != nil {
		v := *d.ReworkEscalationFromAttempt
		d.ReworkEscalationFromAttempt = &v
	}
	return d
}

func (r *Repo) UnmarshalJSON(data []byte) error {
	type plain Repo
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var raw configtypes.SettingsValues
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range []string{"schema", "id", "name", "root", "branch", "remote_url", "updated_at", "run_defaults"} {
		delete(raw, key)
	}
	*r = Repo(value)
	if len(raw) > 0 {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		r.extra = string(encoded)
	}
	return nil
}

func (r Repo) MarshalJSON() ([]byte, error) {
	type plain Repo
	if r.extra == "" {
		return json.Marshal(plain(r))
	}
	data, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	raw := configtypes.SettingsValues{}
	if err := json.Unmarshal([]byte(r.extra), &raw); err != nil {
		return nil, err
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(data, &known); err != nil {
		return nil, err
	}
	for k, v := range known {
		raw[k] = v
	}
	return json.Marshal(raw)
}
