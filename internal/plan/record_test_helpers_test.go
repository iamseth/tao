package plan

func testRecord(planDir string, detail *PlanDetail) *PlanRecord {
	record, err := NewPlanRecord(planDir, detail)
	if err != nil {
		panic(err)
	}
	return record
}

func testRepoRecord(repo PlanRecordStore, detail *PlanDetail) *PlanRecord {
	record, err := repo.PlanRecord(detail)
	if err != nil {
		panic(err)
	}
	return record
}
