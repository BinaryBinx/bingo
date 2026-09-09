package core

// File loading accepts the original exported Go field names alongside the
// canonical snake_case names written by SaveConfig. Pointers preserve explicit
// zero/false values and defaults for omitted fields. If both spellings occur,
// the canonical value wins regardless of their order in the JSON object.
type multiCoreFileConfig struct {
	Enabled           *bool `json:"enabled"`
	NumCPU            *int  `json:"num_cpu"`
	WorkersPerCore    *int  `json:"workers_per_core"`
	EnableCPUAffinity *bool `json:"enable_cpu_affinity"`
	MaxConns          *int  `json:"max_conns"`
	ReadBufferSize    *int  `json:"read_buffer_size"`
	WriteBufferSize   *int  `json:"write_buffer_size"`

	LegacyNumCPU            *int  `json:"NumCPU"`
	LegacyWorkersPerCore    *int  `json:"WorkersPerCore"`
	LegacyEnableCPUAffinity *bool `json:"EnableCPUAffinity"`
	LegacyMaxConns          *int  `json:"MaxConns"`
	LegacyReadBufferSize    *int  `json:"ReadBufferSize"`
	LegacyWriteBufferSize   *int  `json:"WriteBufferSize"`
}

func (f *multiCoreFileConfig) apply(c *MultiCoreConfig) {
	if f.Enabled != nil {
		c.Enabled = *f.Enabled
	}
	for _, field := range []struct {
		current, legacy, target *int
	}{
		{f.NumCPU, f.LegacyNumCPU, &c.NumCPU},
		{f.WorkersPerCore, f.LegacyWorkersPerCore, &c.WorkersPerCore},
		{f.MaxConns, f.LegacyMaxConns, &c.MaxConns},
		{f.ReadBufferSize, f.LegacyReadBufferSize, &c.ReadBufferSize},
		{f.WriteBufferSize, f.LegacyWriteBufferSize, &c.WriteBufferSize},
	} {
		value := field.current
		if value == nil {
			value = field.legacy
		}
		if value != nil {
			*field.target = *value
		}
	}
	affinity := f.EnableCPUAffinity
	if affinity == nil {
		affinity = f.LegacyEnableCPUAffinity
	}
	if affinity != nil {
		c.EnableCPUAffinity = *affinity
	}
}
