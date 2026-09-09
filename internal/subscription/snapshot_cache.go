package subscription

type instanceSnapshotCache struct {
	instances []*managedInstance
	err       error
}

func (m *Manager) publishInstances(instances []*managedInstance, err error) {
	copy := make([]*managedInstance, 0, len(instances))
	for _, instance := range instances {
		if instance != nil {
			value := *instance
			copy = append(copy, &value)
		}
	}
	m.snapshotCache.Store(&instanceSnapshotCache{instances: copy, err: err})
}

func (m *Manager) refreshSnapshotLocked() {
	instances, err := m.loadInstancesLocked()
	m.publishInstances(instances, err)
}
