// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ddl_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/ddl/notifier"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/planner/cardinality"
	"github.com/pingcap/tidb/pkg/statistics/handle/ddl"
	statstestutil "github.com/pingcap/tidb/pkg/statistics/handle/ddl/testutil"
	"github.com/pingcap/tidb/pkg/statistics/handle/storage"
	statsutil "github.com/pingcap/tidb/pkg/statistics/handle/util"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/pingcap/tidb/pkg/util"
	"github.com/pingcap/tidb/pkg/util/mock"
	"github.com/stretchr/testify/require"
)

func TestDDLAfterLoad(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	testKit.MustExec("use test")
	testKit.MustExec("create table t (c1 int, c2 int, index idx(c1, c2))")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	statsTbl := do.StatsHandle().GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)
	recordCount := 1000
	for i := range recordCount {
		testKit.MustExec("insert into t values (?, ?)", i, i+1)
	}
	testKit.MustExec("analyze table t")
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)
	// add column
	testKit.MustExec("alter table t add column c10 int")
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()

	sctx := mock.NewContext()
	count := cardinality.ColumnGreaterRowCount(sctx, statsTbl, types.NewDatum(recordCount+1), tableInfo.Columns[0].ID)
	require.Equal(t, 0.0, count)
	count = cardinality.ColumnGreaterRowCount(sctx, statsTbl, types.NewDatum(recordCount+1), tableInfo.Columns[2].ID)
	require.Equal(t, 333, int(count))
}

func TestDDLTable(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	testKit.MustExec("use test")
	testKit.MustExec("create table t (c1 int, c2 int)")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	h := do.StatsHandle()
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))
	statsTbl := h.GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)

	testKit.MustExec("create table t1 (c1 int, c2 int, index idx(c1))")
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t1"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))
	statsTbl = h.GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)

	// For FK table's CreateTable Event
	// https://github.com/pingcap/tidb/issues/53652
	testKit.MustExec("create table t_parent (id int primary key)")
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t_parent"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))
	statsTbl = h.GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)

	testKit.MustExec("create table t_child (id int primary key, pid int, foreign key (pid) references t_parent(id) on delete cascade on update cascade);")
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t_child"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))
	statsTbl = h.GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)
}

func TestSystemTableDDLHasNoEvent(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	testKit.MustExec("use test")
	// Test create a system table.
	testKit.MustExec("create table mysql.test (c1 int, c2 int)")
	h := do.StatsHandle()
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("truncate table mysql.test")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("alter table mysql.test add column c3 int")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("alter table mysql.test modify column c1 int")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("drop table mysql.test")
	require.Len(t, h.DDLEventCh(), 0)

	testKit.MustExec("create table mysql.test2 (c1 int, c2 int) partition by range (c1) (partition p0 values less than (6))")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("alter table mysql.test2 add partition (partition p1 values less than (11))")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("alter table mysql.test2 truncate partition p1")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("alter table mysql.test2 drop partition p1")
	require.Len(t, h.DDLEventCh(), 0)
	testKit.MustExec("alter table mysql.test2 remove partitioning")
	require.Len(t, h.DDLEventCh(), 0)

	testKit.MustExec("create table t (c1 int, c2 int, index idx(c1, c2)) partition by range (c1) (partition p0 values less than (6))")
	<-h.DDLEventCh()
	testKit.MustExec("create table mysql.test3 (c1 int, c2 int, index idx(c1, c2))")
	// Exchange partition.
	// NOTE: This is a rare case and the effort required to address it outweighs the benefits, hence it is not prioritized for a fix.
	testKit.MustExec("alter table t exchange partition p0 with table mysql.test3")
	require.Len(t, h.DDLEventCh(), 0)
}

func TestTruncateTable(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	testKit.MustExec("use test")
	testKit.MustExec("create table t (c1 int, c2 int, index idx(c1, c2))")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	h := do.StatsHandle()
	// Insert some data.
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	err = h.Update(context.Background(), do.InfoSchema())
	require.NoError(t, err)
	statsTbl := h.GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)

	// Get stats update version.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?",
		tableInfo.ID,
	).Rows()
	require.Len(t, rows, 1)
	version := rows[0][0].(string)

	// Truncate table.
	testKit.MustExec("truncate table t")

	// Find the truncate table partition event.
	truncateTableEvent := findEvent(h.DDLEventCh(), model.ActionTruncateTable)
	err = statstestutil.HandleDDLEventWithTxn(h, truncateTableEvent)
	require.NoError(t, err)

	// Get new table info.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	newTableInfo := tbl.Meta()
	// Get new added table's stats meta.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", newTableInfo.ID,
	).Rows()
	require.Len(t, rows, 1)

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Rows()
	require.Len(t, rows, 1)
	// Version gets updated after truncate the table.
	require.NotEqual(t, version, rows[0][0].(string))
}

func TestTruncateAPartitionedTable(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	require.NotNil(t, pi)
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0's and p1's stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)

	// Truncate the whole table.
	testKit.MustExec("truncate table t")
	// Find the truncate table event.
	truncateTableEvent := findEvent(h.DDLEventCh(), model.ActionTruncateTable)
	err = statstestutil.HandleDDLEventWithTxn(h, truncateTableEvent)
	require.NoError(t, err)

	// Get new table info.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	newTableInfo := tbl.Meta()
	// Get all new added partitions ID.
	newPartitionIDs := make([]int64, 0, len(newTableInfo.Partition.Definitions))
	for _, def := range newTableInfo.Partition.Definitions {
		newPartitionIDs = append(newPartitionIDs, def.ID)
	}
	// Check new added table's stats meta.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", newPartitionIDs[0], newPartitionIDs[1],
	).Rows()
	require.Len(t, rows, 2)

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	// Version gets updated after truncate the table.
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
}

func TestDDLHistogram(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()

	testKit.MustExec("use test")
	testKit.MustExec("create table t (c1 int, c2 int, index idx(c1, c2))")
	<-h.DDLEventCh()
	testKit.MustExec("insert into t values(1,2),(3,4)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	require.Nil(t, h.Update(context.Background(), is))
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	statsTbl := do.StatsHandle().GetTableStats(tableInfo)
	lastHistUpdateVersion1 := statsTbl.LastStatsHistVersion

	testKit.MustExec("alter table t add column c_null int")
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)

	// Check that the last_stats_histograms_version has been updated.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	require.Nil(t, h.Update(context.Background(), is))
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	lastHistUpdateVersion2 := statsTbl.LastStatsHistVersion
	require.Greater(t, lastHistUpdateVersion2, lastHistUpdateVersion1)

	require.True(t, statsTbl.ColAndIdxExistenceMap.HasAnalyzed(2, false))
	require.False(t, statsTbl.Pseudo)
	require.True(t, statsTbl.GetCol(tableInfo.Columns[2].ID).IsStatsInitialized())
	require.Equal(t, int64(2), statsTbl.GetCol(tableInfo.Columns[2].ID).NullCount)
	require.Equal(t, int64(0), statsTbl.GetCol(tableInfo.Columns[2].ID).Histogram.NDV)

	testKit.MustExec("alter table t add column c3 int NOT NULL")
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	is = do.InfoSchema()
	require.Nil(t, h.Update(context.Background(), is))
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)
	require.True(t, statsTbl.ColAndIdxExistenceMap.HasAnalyzed(3, false))
	require.True(t, statsTbl.GetCol(tableInfo.Columns[3].ID).IsStatsInitialized())
	sctx := mock.NewContext()
	count, err := cardinality.ColumnEqualRowCount(sctx, statsTbl, types.NewIntDatum(0), tableInfo.Columns[3].ID)
	require.NoError(t, err)
	require.Equal(t, float64(2), count)
	count, err = cardinality.ColumnEqualRowCount(sctx, statsTbl, types.NewIntDatum(1), tableInfo.Columns[3].ID)
	require.NoError(t, err)
	require.Equal(t, float64(0), count)

	testKit.MustExec("alter table t add column c4 datetime NOT NULL default CURRENT_TIMESTAMP")
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	is = do.InfoSchema()
	require.Nil(t, h.Update(context.Background(), is))
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	// If we don't use original default value, we will get a pseudo table.
	require.False(t, statsTbl.Pseudo)
	require.True(t, statsTbl.ColAndIdxExistenceMap.HasAnalyzed(4, false))

	testKit.MustExec("alter table t add column c5 varchar(15) DEFAULT '123'")
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	is = do.InfoSchema()
	require.Nil(t, h.Update(context.Background(), is))
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)
	require.True(t, statsTbl.ColAndIdxExistenceMap.HasAnalyzed(5, false))
	require.True(t, statsTbl.GetCol(tableInfo.Columns[5].ID).IsStatsInitialized())
	require.Equal(t, 3.0, cardinality.AvgColSize(statsTbl.GetCol(tableInfo.Columns[5].ID), statsTbl.RealtimeCount, false))

	testKit.MustExec("alter table t add column c6 varchar(15) DEFAULT '123', add column c7 varchar(15) DEFAULT '123'")
	err = statstestutil.HandleNextDDLEventWithTxn(h)
	require.NoError(t, err)
	is = do.InfoSchema()
	require.Nil(t, h.Update(context.Background(), is))
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	require.False(t, statsTbl.Pseudo)

	testKit.MustExec("create index i on t(c2, c1)")
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	require.False(t, statsTbl.ColAndIdxExistenceMap.HasAnalyzed(2, true))
	testKit.MustExec("analyze table t")
	tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	statsTbl = do.StatsHandle().GetTableStats(tableInfo)
	require.True(t, statsTbl.ColAndIdxExistenceMap.HasAnalyzed(1, true))
	rs := testKit.MustQuery("select count(*) from mysql.stats_histograms where table_id = ? and hist_id = 1 and is_index =1", tableInfo.ID)
	rs.Check(testkit.Rows("1"))
	rs = testKit.MustQuery("select count(*) from mysql.stats_buckets where table_id = ? and hist_id = 1 and is_index = 1", tableInfo.ID)
	rs.Check(testkit.Rows("0"))
	rs = testKit.MustQuery("select count(*) from mysql.stats_top_n where table_id = ? and hist_id = 1 and is_index = 1", tableInfo.ID)
	rs.Check(testkit.Rows("2"))
}

func TestDDLPartition(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	for i, pruneMode := range []string{"static", "dynamic"} {
		testKit.MustExec("set @@tidb_partition_prune_mode=`" + pruneMode + "`")
		testKit.MustExec("set global tidb_partition_prune_mode=`" + pruneMode + "`")
		testKit.MustExec("use test")
		testKit.MustExec("drop table if exists t")
		h := do.StatsHandle()
		if i == 1 {
			err := statstestutil.HandleNextDDLEventWithTxn(h)
			require.NoError(t, err)
		}
		createTable := `CREATE TABLE t (a int, b int, primary key(a), index idx(b))
PARTITION BY RANGE ( a ) (
		PARTITION p0 VALUES LESS THAN (6),
		PARTITION p1 VALUES LESS THAN (11),
		PARTITION p2 VALUES LESS THAN (16),
		PARTITION p3 VALUES LESS THAN (21)
)`
		testKit.MustExec(createTable)
		is := do.InfoSchema()
		tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
		require.NoError(t, err)
		tableInfo := tbl.Meta()
		err = statstestutil.HandleNextDDLEventWithTxn(h)
		require.NoError(t, err)
		require.Nil(t, h.Update(context.Background(), is))
		pi := tableInfo.GetPartitionInfo()
		for _, def := range pi.Definitions {
			statsTbl := h.GetPartitionStats(tableInfo, def.ID)
			require.False(t, statsTbl.Pseudo, "for %v", pruneMode)
		}

		testKit.MustExec("insert into t values (1,2),(6,2),(11,2),(16,2)")
		testKit.MustExec("analyze table t")
		testKit.MustExec("alter table t add column c varchar(15) DEFAULT '123'")
		err = statstestutil.HandleNextDDLEventWithTxn(h)
		require.NoError(t, err)
		is = do.InfoSchema()
		require.Nil(t, h.Update(context.Background(), is))
		tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
		require.NoError(t, err)
		tableInfo = tbl.Meta()
		pi = tableInfo.GetPartitionInfo()
		for _, def := range pi.Definitions {
			statsTbl := h.GetPartitionStats(tableInfo, def.ID)
			require.False(t, statsTbl.Pseudo)
			require.Equal(t, 3.0, cardinality.AvgColSize(statsTbl.GetCol(tableInfo.Columns[2].ID), statsTbl.RealtimeCount, false))
		}

		addPartition := "alter table t add partition (partition p4 values less than (26))"
		testKit.MustExec(addPartition)
		is = do.InfoSchema()
		tbl, err = is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
		require.NoError(t, err)
		tableInfo = tbl.Meta()
		err = statstestutil.HandleNextDDLEventWithTxn(h)
		require.NoError(t, err)
		require.Nil(t, h.Update(context.Background(), is))
		pi = tableInfo.GetPartitionInfo()
		for _, def := range pi.Definitions {
			statsTbl := h.GetPartitionStats(tableInfo, def.ID)
			require.False(t, statsTbl.Pseudo)
		}
	}
}

func TestReorgPartitions(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)
	// Get all the partition IDs.
	partitionIDs := make(map[int64]struct{}, len(pi.Definitions))
	for _, def := range pi.Definitions {
		partitionIDs[def.ID] = struct{}{}
	}

	// Get partition p0 and p1's stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)

	// Reorganize two partitions.
	testKit.MustExec("alter table t reorganize partition p0, p1 into (partition p0 values less than (11))")
	// Find the reorganize partition event.
	reorganizePartitionEvent := findEvent(h.DDLEventCh(), model.ActionReorganizePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, reorganizePartitionEvent)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
}

func TestIncreasePartitionCountOfHashPartitionTable(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()

	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec("create table t (a int, b int, index idx(a, b)) partition by hash(a) partitions 2")
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0 and p1's stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)

	// Increase the partition count to 4.
	testKit.MustExec("alter table t add partition partitions 2")
	// Find the reorganize partition event.
	reorganizePartitionEvent := findEvent(h.DDLEventCh(), model.ActionReorganizePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, reorganizePartitionEvent)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))

	// Check new partitions are added.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	pi = tableInfo.GetPartitionInfo()
	require.Len(t, pi.Definitions, 4)
	// Check the stats meta.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?, ?, ?) order by table_id",
		pi.Definitions[0].ID, pi.Definitions[1].ID, pi.Definitions[2].ID, pi.Definitions[3].ID,
	).Rows()
	require.Len(t, rows, 4)

	// Check the old partitions' stats version is changed.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
}

func TestDecreasePartitionCountOfHashPartitionTable(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()

	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec("create table t (a int, b int, index idx(a, b)) partition by hash(a) partitions 4")
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	require.Len(t, pi.Definitions, 4)
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0 and p1's stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	partitionP2ID := pi.Definitions[2].ID
	partitionP3ID := pi.Definitions[3].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?, ?, ?) order by table_id",
		partitionP0ID, partitionP1ID, partitionP2ID, partitionP3ID,
	).Rows()
	require.Len(t, rows, 4)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)
	versionP2 := rows[2][0].(string)
	versionP3 := rows[3][0].(string)

	// Decrease the partition count to 2.
	testKit.MustExec("alter table t coalesce partition 2")
	// Find the reorganize partition event.
	reorganizePartitionEvent := findEvent(h.DDLEventCh(), model.ActionReorganizePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, reorganizePartitionEvent)
	require.NoError(t, err)
	require.Nil(t, h.Update(context.Background(), is))

	// Check new partitions are added.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	pi = tableInfo.GetPartitionInfo()
	require.Len(t, pi.Definitions, 2)
	// Check the stats meta.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id",
		pi.Definitions[0].ID, pi.Definitions[1].ID,
	).Rows()
	require.Len(t, rows, 2)

	// Check the old partitions' stats version is changed.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?, ?, ?) order by table_id",
		partitionP0ID, partitionP1ID, partitionP2ID, partitionP3ID,
	).Rows()
	require.Len(t, rows, 4)
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
	require.NotEqual(t, versionP2, rows[2][0].(string))
	require.NotEqual(t, versionP3, rows[3][0].(string))
}

func TestTruncateAPartition(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0's stats update version.
	partitionID := pi.Definitions[0].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	version := rows[0][0].(string)

	testKit.MustExec("alter table t truncate partition p0")
	// Find the truncate partition event.
	truncatePartitionEvent := findEvent(h.DDLEventCh(), model.ActionTruncateTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, truncatePartitionEvent)
	require.NoError(t, err)
	// Check global stats meta.
	// Because we have truncated a partition, the count should be 5 - 2 = 3 and the modify count should be 2.
	testKit.MustQuery(
		"select count, modify_count from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Check(
		testkit.Rows("3 2"),
	)

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	// Version gets updated after truncate the partition.
	require.NotEqual(t, version, rows[0][0].(string))
}

func TestTruncateAPartitionAndDropTableImmediately(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0's stats update version.
	partitionID := pi.Definitions[0].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	version := rows[0][0].(string)

	testKit.MustExec("alter table t truncate partition p0")
	testKit.MustExec("drop table t")
	// Find the truncate partition event.
	truncatePartitionEvent := findEvent(h.DDLEventCh(), model.ActionTruncateTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, truncatePartitionEvent)
	require.NoError(t, err)
	// Check global stats meta.
	// Because we have truncated a partition, the count should be 5 - 2 = 3 and the modify count should be 2.
	testKit.MustQuery(
		"select count, modify_count from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Check(
		testkit.Rows("3 2"),
	)

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	// Version gets updated after truncate the partition.
	require.NotEqual(t, version, rows[0][0].(string))
}

func TestTruncateAHashPartition(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a bigint,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by hash(a) partitions 4
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	require.NotNil(t, pi)
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0's stats update version.
	partitionID := pi.Definitions[0].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	version := rows[0][0].(string)

	testKit.MustExec("alter table t truncate partition p0")
	// Find the truncate partition event.
	truncatePartitionEvent := findEvent(h.DDLEventCh(), model.ActionTruncateTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, truncatePartitionEvent)
	require.NoError(t, err)
	// Check global stats meta.
	// Because we have truncated a partition, the count should be 5 - 1 = 4 and the modify count should be 1.
	testKit.MustQuery(
		"select count, modify_count from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Check(
		testkit.Rows("4 1"),
	)

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	// Version gets updated after truncate the partition.
	require.NotEqual(t, version, rows[0][0].(string))
}

func TestTruncatePartitions(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0 and p1's stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)

	// Truncate two partitions.
	testKit.MustExec("alter table t truncate partition p0, p1")
	// Find the truncate partition event.
	truncatePartitionEvent := findEvent(h.DDLEventCh(), model.ActionTruncateTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, truncatePartitionEvent)
	require.NoError(t, err)
	// Check global stats meta.
	// Because we have truncated two partitions, the count should be 5 - 2 - 1  = 2 and the modify count should be 3.
	testKit.MustQuery(
		"select count, modify_count from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Check(
		testkit.Rows("2 3"),
	)

	// Check the version again.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id", partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	// Version gets updated after truncate the partition.
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
}

func TestDropAPartition(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	testKit.MustExec("alter table t drop partition p0")
	// Find the drop partition event.
	dropPartitionEvent := findEvent(h.DDLEventCh(), model.ActionDropTablePartition)

	// Get partition p0's stats update version.
	partitionID := pi.Definitions[0].ID
	// Get it from stats_meta first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", partitionID,
	).Rows()
	require.Len(t, rows, 1)
	version := rows[0][0].(string)

	err = statstestutil.HandleDDLEventWithTxn(h, dropPartitionEvent)
	require.NoError(t, err)
	// Check the global stats meta.
	// Because we have dropped a partition, the count should be 3 and the modify count should be 2.
	testKit.MustQuery(
		"select count, modify_count from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Check(
		testkit.Rows("3 2"),
	)

	// Check the update version is changed.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Rows()
	require.Len(t, rows, 1)
	require.NotEqual(t, version, rows[0][0].(string))
}

func TestDropPartitions(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	err = h.Update(context.Background(), is)
	require.NoError(t, err)

	// Get partition p0 and p1's stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	// Get it from stats_meat first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id",
		partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)

	// Drop partition p0 and p1.
	testKit.MustExec("alter table t drop partition p0,p1")
	// Find the drop partition event.
	dropPartitionEvent := findEvent(h.DDLEventCh(), model.ActionDropTablePartition)

	err = statstestutil.HandleDDLEventWithTxn(h, dropPartitionEvent)
	require.NoError(t, err)

	// Check the global stats meta.
	// Because we have dropped two partitions,
	// the count should be 5 - 2 - 1 = 2 and the modify count should be 2 +1 = 3.
	testKit.MustQuery(
		"select count, modify_count from mysql.stats_meta where table_id = ?", tableInfo.ID,
	).Check(
		testkit.Rows("2 3"),
	)

	// Check the update versions are changed.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?) order by table_id",
		partitionP0ID, partitionP1ID,
	).Rows()
	require.Len(t, rows, 2)
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
}

func TestExchangeAPartition(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	// Create a table with 4 partitions.
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	h.DumpStatsDeltaToKV(true)

	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	// Create a normal table to exchange partition.
	testKit.MustExec("drop table if exists t1")
	testKit.MustExec("create table t1 (a int, b int, primary key(a), index idx(b))")
	// Insert some data which meets the condition of the partition p0.
	testKit.MustExec("insert into t1 values (1,2),(2,2),(3,2),(4,2),(5,2)")
	err = h.DumpStatsDeltaToKV(true)
	require.NoError(t, err)

	testKit.MustExec("analyze table t1")
	is = do.InfoSchema()
	tbl1, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t1"),
	)
	require.NoError(t, err)
	tableInfo1 := tbl1.Meta()
	statsTbl1 := h.GetTableStats(tableInfo1)
	require.False(t, statsTbl1.Pseudo)

	// Check the global stats meta before exchange partition.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("5 0"),
	)

	// Exchange partition p0 with table t1.
	testKit.MustExec("alter table t exchange partition p0 with table t1")
	// Find the exchange partition event.
	exchangePartitionEvent := findEvent(h.DDLEventCh(), model.ActionExchangeTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, exchangePartitionEvent)
	require.NoError(t, err)
	// Check the global stats meta.
	// Because we have exchanged a partition, the count should be 5 and the modify count should be 5(table) + 2(partition).
	// 5 -> Five rows are added to table 't' as 't1' is included as a new partition.
	// 2 -> Two rows are removed from table 't' as partition 'p0' is no longer a part of it.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("8 7"),
	)

	// Create another normal table with no data to exchange partition.
	testKit.MustExec("drop table if exists t2")
	testKit.MustExec("create table t2 (a int, b int, primary key(a), index idx(b))")
	testKit.MustExec("analyze table t2")
	is = do.InfoSchema()
	tbl2, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t2"),
	)
	require.NoError(t, err)
	tableInfo2 := tbl2.Meta()
	statsTbl2 := h.GetTableStats(tableInfo2)
	require.False(t, statsTbl2.Pseudo)
	err = h.Update(context.Background(), do.InfoSchema())
	require.NoError(t, err)

	// Insert some data to partition p1 before exchange partition.
	testKit.MustExec("insert into t values (7,2),(8,2),(9,2),(10,2)")
	err = h.DumpStatsDeltaToKV(true)
	require.NoError(t, err)
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		// modify_count = 7 + 4 = 11
		testkit.Rows("12 11"),
	)

	testKit.MustExec("alter table t exchange partition p1 with table t2")
	// Find the exchange partition event.
	exchangePartitionEvent = findEvent(h.DDLEventCh(), model.ActionExchangeTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, exchangePartitionEvent)
	require.NoError(t, err)
	// Check the global stats meta.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		// count = 12 - 5(old partition) + 0(new table) = 7
		// modify_count = 11 + 5(old partition) + 0(new table) - 4(old partition) = 12
		// 5 -> Five rows are removed from table 't' as partition 'p1' is no longer a part of it.
		// 0 -> No rows are added to table 't' as 't2' is added as a partition to it.
		// 4 -> Four rows are subtracted from table 't' due to the insertion of four rows into partition 'p1'.
		testkit.Rows("7 12"),
	)

	// Test if the global stats is accidentally dropped.
	// Create another normal table with no data to exchange partition.
	testKit.MustExec("drop table if exists t3")
	testKit.MustExec("create table t3 (a int, b int, primary key(a), index idx(b))")
	testKit.MustExec("analyze table t3")
	is = do.InfoSchema()
	tbl3, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t3"),
	)
	require.NoError(t, err)
	tableInfo3 := tbl3.Meta()
	statsTbl3 := h.GetTableStats(tableInfo3)
	require.False(t, statsTbl3.Pseudo)
	err = h.Update(context.Background(), do.InfoSchema())
	require.NoError(t, err)

	testKit.MustExec("alter table t exchange partition p2 with table t3")
	// Drop the global stats.
	testKit.MustExec(fmt.Sprintf("delete from mysql.stats_meta where table_id = %d", tableInfo.ID))
	// Find the exchange partition event.
	exchangePartitionEvent = findEvent(h.DDLEventCh(), model.ActionExchangeTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, exchangePartitionEvent)
	require.NoError(t, err)
	// Check the global stats meta.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		// Insert the global stats back.
		testkit.Rows("0 1"),
	)
}

func TestExchangeAPartitionAndDropTableImmediately(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	// Create a table with 4 partitions.
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	h.DumpStatsDeltaToKV(true)

	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}
	// Create a normal table to exchange partition.
	testKit.MustExec("drop table if exists t1")
	testKit.MustExec("create table t1 (a int, b int, primary key(a), index idx(b))")
	// Insert some data which meets the condition of the partition p0.
	testKit.MustExec("insert into t1 values (1,2),(2,2),(3,2),(4,2),(5,2)")
	err = h.DumpStatsDeltaToKV(true)
	require.NoError(t, err)

	testKit.MustExec("analyze table t1")
	is = do.InfoSchema()
	tbl1, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t1"),
	)
	require.NoError(t, err)
	tableInfo1 := tbl1.Meta()
	statsTbl1 := h.GetTableStats(tableInfo1)
	require.False(t, statsTbl1.Pseudo)

	// Check the global stats meta before exchange partition.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("5 0"),
	)

	// Exchange partition p0 with table t1.
	testKit.MustExec("alter table t exchange partition p0 with table t1")
	testKit.MustExec("drop table t")
	testKit.MustExec("drop table t1")
	// Find the exchange partition event.
	exchangePartitionEvent := findEvent(h.DDLEventCh(), model.ActionExchangeTablePartition)
	err = statstestutil.HandleDDLEventWithTxn(h, exchangePartitionEvent)
	require.NoError(t, err)
	// Check the global stats meta.
	// Because we have exchanged a partition, the count should be 5 and the modify count should be 5(table) + 2(partition).
	// 5 -> Five rows are added to table 't' as 't1' is included as a new partition.
	// 2 -> Two rows are removed from table 't' as partition 'p0' is no longer a part of it.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("8 7"),
	)
}

func TestRemovePartitioning(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	// Create a table with 4 partitions.
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
		partition by range (a) (
			partition p0 values less than (6),
			partition p1 values less than (11),
			partition p2 values less than (16),
			partition p3 values less than (21)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	h.DumpStatsDeltaToKV(true)

	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	pi := tableInfo.GetPartitionInfo()
	for _, def := range pi.Definitions {
		statsTbl := h.GetPartitionStats(tableInfo, def.ID)
		require.False(t, statsTbl.Pseudo)
	}

	// Get all partitions' stats update version.
	partitionP0ID := pi.Definitions[0].ID
	partitionP1ID := pi.Definitions[1].ID
	partitionP2ID := pi.Definitions[2].ID
	partitionP3ID := pi.Definitions[3].ID
	// Get it from stats_meta first.
	rows := testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?, ?, ?) order by table_id",
		partitionP0ID, partitionP1ID, partitionP2ID, partitionP3ID,
	).Rows()
	require.Len(t, rows, 4)
	versionP0 := rows[0][0].(string)
	versionP1 := rows[1][0].(string)
	versionP2 := rows[2][0].(string)
	versionP3 := rows[3][0].(string)

	// Remove partitioning.
	testKit.MustExec("alter table t remove partitioning")
	// Find the remove partitioning event.
	removePartitioningEvent := findEvent(h.DDLEventCh(), model.ActionRemovePartitioning)
	err = statstestutil.HandleDDLEventWithTxn(h, removePartitioningEvent)
	require.NoError(t, err)
	// Check the global stats meta make sure the count and modify count are not changed.
	// Get new table id after remove partitioning.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("5 0"),
	)

	// Check the update versions are changed.
	rows = testKit.MustQuery(
		"select version from mysql.stats_meta where table_id in (?, ?, ?, ?) order by table_id",
		partitionP0ID, partitionP1ID, partitionP2ID, partitionP3ID,
	).Rows()
	require.Len(t, rows, 4)
	require.NotEqual(t, versionP0, rows[0][0].(string))
	require.NotEqual(t, versionP1, rows[1][0].(string))
	require.NotEqual(t, versionP2, rows[2][0].(string))
	require.NotEqual(t, versionP3, rows[3][0].(string))
}

func TestAddPartitioning(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	h := do.StatsHandle()
	testKit.MustExec("use test")
	testKit.MustExec("drop table if exists t")
	// Create a table without partitioning.
	testKit.MustExec(`
		create table t (
			a int,
			b int,
			primary key(a),
			index idx(b)
		)
	`)
	testKit.MustExec("insert into t values (1,2),(2,2),(6,2),(11,2),(16,2)")
	h.DumpStatsDeltaToKV(true)
	testKit.MustExec("analyze table t")
	is := do.InfoSchema()
	tbl, err := is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	// Check the global stats meta before add partitioning.
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("5 0"),
	)

	// Add partitioning.
	testKit.MustExec("alter table t partition by hash(a) partitions 3")
	// Find the add partitioning event.
	addPartitioningEvent := findEvent(h.DDLEventCh(), model.ActionAlterTablePartitioning)
	err = statstestutil.HandleDDLEventWithTxn(h, addPartitioningEvent)
	require.NoError(t, err)
	// Check the global stats meta make sure the count and modify count are not changed.
	// Get new table id after remove partitioning.
	is = do.InfoSchema()
	tbl, err = is.TableByName(context.Background(),
		ast.NewCIStr("test"), ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	tableInfo = tbl.Meta()
	testKit.MustQuery(
		fmt.Sprintf("select count, modify_count from mysql.stats_meta where table_id = %d", tableInfo.ID),
	).Check(
		testkit.Rows("5 0"),
	)
}

func TestDropSchema(t *testing.T) {
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)

	tk.MustExec("use test")
	tk.MustExec("create table t (c1 int)")
	h := dom.StatsHandle()
	tk.MustExec("insert into t values (1)")
	require.NoError(t, h.DumpStatsDeltaToKV(true))

	is := dom.InfoSchema()
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableInfo := tbl.Meta()
	// Check the current stats meta version.
	rows := tk.MustQuery(
		"select version from mysql.stats_meta where table_id = ?",
		tableInfo.ID,
	).Rows()
	require.Len(t, rows, 1)
	version := rows[0][0].(string)

	tk.MustExec("drop database test")

	// Handle the drop schema event.
	dropSchemaEvent := findEvent(h.DDLEventCh(), model.ActionDropSchema)
	err = statstestutil.HandleDDLEventWithTxn(h, dropSchemaEvent)
	require.NoError(t, err)

	// Check the stats meta version after drop schema.
	rows = tk.MustQuery(
		"select version from mysql.stats_meta where table_id = ?",
		tableInfo.ID,
	).Rows()
	require.Len(t, rows, 1)
	require.NotEqual(t, version, rows[0][0].(string))
}

func findEvent(eventCh <-chan *notifier.SchemaChangeEvent, eventType model.ActionType) *notifier.SchemaChangeEvent {
	// Find the target event.
	for {
		event := <-eventCh
		if event.GetType() == eventType {
			return event
		}
	}
}

func TestExchangePartition(t *testing.T) {
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)

	tk.MustExec("use test")
	tk.MustExec("create table t (c1 int)")
	is := dom.InfoSchema()
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	var wg util.WaitGroupWrapper
	for range 20 {
		tk1 := testkit.NewTestKit(t, store)
		wg.Run(func() {
			tk1.MustExec("begin")
			ddl.UpdateStatsWithCountDeltaAndModifyCountDeltaForTest(tk1.Session(), tbl.Meta().ID, 10, 10)
			tk1.MustExec("commit")
		})
	}
	wg.Wait()
	count, modifyCount, isNull, err := storage.StatsMetaCountAndModifyCount(statsutil.StatsCtx, tk.Session(), tbl.Meta().ID)
	require.NoError(t, err)
	require.False(t, isNull)
	require.Equal(t, int64(200), count)
	require.Equal(t, int64(200), modifyCount)
}

func TestDumpStatsDeltaBeforeHandleDDLEvent(t *testing.T) {
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table t (c1 int)")
	// Insert some data.
	tk.MustExec("insert into t values (1), (2), (3)")
	h := dom.StatsHandle()
	require.NoError(t, h.DumpStatsDeltaToKV(true))
	// Also manually insert a histogram record.
	is := dom.InfoSchema()
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	_, err = tk.Exec("insert into mysql.stats_histograms (table_id, is_index, hist_id, distinct_count, version) values (?, 0, ?, 0, ?)", tbl.Meta().ID, 1, 1)
	require.NoError(t, err)
	// Find the DDL event.
	event := findEvent(h.DDLEventCh(), model.ActionCreateTable)
	err = statstestutil.HandleDDLEventWithTxn(h, event)
	require.NoError(t, err)
}

func TestDumpStatsDeltaBeforeHandleAddColumnEvent(t *testing.T) {
	store, do := testkit.CreateMockStoreAndDomain(t)
	testKit := testkit.NewTestKit(t, store)
	testKit.MustExec("use test")
	testKit.MustExec("create table t (c1 int, c2 int, index idx(c1, c2))")
	// Insert some data.
	testKit.MustExec("insert into t values (1, 2), (2, 3), (3, 4)")
	testKit.MustExec("analyze table t")
	// Add column.
	testKit.MustExec("alter table t add column c10 int")
	// Insert some data.
	testKit.MustExec("insert into t values (4, 5, 6)")
	// Analyze table to force create the histogram meta record.
	testKit.MustExec("analyze table t")
	// Find the add column event.
	event := findEvent(do.StatsHandle().DDLEventCh(), model.ActionAddColumn)
	err := statstestutil.HandleDDLEventWithTxn(do.StatsHandle(), event)
	require.NoError(t, err)
}
