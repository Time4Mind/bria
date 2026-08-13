package domain

func (p UserPreferences) EffectiveOfflineInputQueueLimit() int {
	if p.OfflineInputQueueLimit == 0 {
		return 5
	}
	return p.OfflineInputQueueLimit
}
