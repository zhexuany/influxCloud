package meta

import (
	"fmt"
	"io"
	"io/ioutil"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/hashicorp/raft"
	"github.com/influxdata/influxdb/influxql"
	"github.com/influxdata/influxdb/services/meta"
	"github.com/zhexuany/influxcloud/meta/internal"
)

// storeFSM represents the finite state machine used by Store to interact with Raft.
type storeFSM store

func (fsm *storeFSM) Apply(l *raft.Log) interface{} {
	var cmd internal.Command
	if err := proto.Unmarshal(l.Data, &cmd); err != nil {
		panic(fmt.Errorf("cannot marshal command: %x", l.Data))
	}

	// Lock the store.
	s := (*store)(fsm)
	s.mu.Lock()
	defer s.mu.Unlock()

	err := func() interface{} {
		switch cmd.GetType() {
		case internal.Command_CreateDatabaseCommand:
			return fsm.applyCreateDatabaseCommand(&cmd)
		case internal.Command_DropDatabaseCommand:
			return fsm.applyDropDatabaseCommand(&cmd)
		case internal.Command_CreateRetentionPolicyCommand:
			return fsm.applyCreateRetentionPolicyCommand(&cmd)
		case internal.Command_DropRetentionPolicyCommand:
			return fsm.applyDropRetentionPolicyCommand(&cmd)
		case internal.Command_SetDefaultRetentionPolicyCommand:
			return fsm.applySetDefaultRetentionPolicyCommand(&cmd)
		case internal.Command_UpdateRetentionPolicyCommand:
			return fsm.applyUpdateRetentionPolicyCommand(&cmd)
		case internal.Command_CreateShardGroupCommand:
			return fsm.applyCreateShardGroupCommand(&cmd)
		case internal.Command_DeleteShardGroupCommand:
			return fsm.applyDeleteShardGroupCommand(&cmd)
		case internal.Command_CreateContinuousQueryCommand:
			return fsm.applyCreateContinuousQueryCommand(&cmd)
		case internal.Command_DropContinuousQueryCommand:
			return fsm.applyDropContinuousQueryCommand(&cmd)
		case internal.Command_CreateSubscriptionCommand:
			return fsm.applyCreateSubscriptionCommand(&cmd)
		case internal.Command_DropSubscriptionCommand:
			return fsm.applyDropSubscriptionCommand(&cmd)
		case internal.Command_CreateUserCommand:
			return fsm.applyCreateUserCommand(&cmd)
		case internal.Command_DropUserCommand:
			return fsm.applyDropUserCommand(&cmd)
		case internal.Command_UpdateUserCommand:
			return fsm.applyUpdateUserCommand(&cmd)
		case internal.Command_SetPrivilegeCommand:
			return fsm.applySetPrivilegeCommand(&cmd)
		case internal.Command_SetAdminPrivilegeCommand:
			return fsm.applySetAdminPrivilegeCommand(&cmd)
		case internal.Command_SetDataCommand:
			return fsm.applySetDataCommand(&cmd)
		case internal.Command_CreateMetaNodeCommand:
			return fsm.applyCreateMetaNodeCommand(&cmd)
		case internal.Command_DeleteMetaNodeCommand:
			return fsm.applyDeleteMetaNodeCommand(&cmd, s)
		case internal.Command_SetMetaNodeCommand:
			return fsm.applySetMetaNodeCommand(&cmd)
		case internal.Command_CreateDataNodeCommand:
			return fsm.applyCreateDataNodeCommand(&cmd)
		case internal.Command_DeleteDataNodeCommand:
			return fsm.applyDeleteDataNodeCommand(&cmd)
		case internal.Command_AddShardOwnerCommand:
			// return fsm.applyAddShardOwnerCommand(&cmd)
		default:
			panic(fmt.Errorf("cannot apply command: %x", l.Data))
		}
		return nil
	}()

	// Copy term and index to new metadata.
	fsm.data.Data.Term = l.Term
	fsm.data.Data.Index = l.Index

	// signal that the data changed
	close(s.dataChanged)
	s.dataChanged = make(chan struct{})

	return err
}

func (fsm *storeFSM) applyUpdateDataNodeCommand(cmd *internal.Command) interface{} {
	ext, err := proto.GetExtension(cmd, internal.E_CreateDataNodeCommand_Command)
	if err != nil {
		return nil
	}
	v := ext.(*internal.UpdateDataNodeCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	node := other.DataNode(v.GetID())
	if node == nil {
		return nil
	}

	node.Host = v.GetHost()
	node.TCPHost = v.GetTCPHost()

	fsm.data = other
	return nil
}

func (fsm *storeFSM) applyCreateDatabaseCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateDatabaseCommand_Command)
	v := ext.(*internal.CreateDatabaseCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.CreateDatabase(v.GetName()); err != nil {
		return err
	}

	s := (*store)(fsm)
	rpi := meta.RetentionPolicyInfo{}
	err := rpi.UnmarshalBinary(v.GetRetentionPolicy())
	if err == nil {
		if err := other.CreateRetentionPolicy(v.GetName(), &rpi, true); err != nil {
			if err == ErrRetentionPolicyExists {
				return ErrRetentionPolicyConflict
			}
			return err
		}
	} else if s.config.RetentionAutoCreate {
		// Read node count.
		// Retention policies must be fully replicated.
		replicaN := len(other.DataNodes)
		if replicaN > maxAutoCreatedRetentionPolicyReplicaN {
			replicaN = maxAutoCreatedRetentionPolicyReplicaN
		} else if replicaN < 1 {
			replicaN = 1
		}

		// Create a retention policy.
		rpi := meta.NewRetentionPolicyInfo(autoCreateRetentionPolicyName)
		rpi.ReplicaN = replicaN
		rpi.Duration = autoCreateRetentionPolicyPeriod
		if err := other.Data.CreateRetentionPolicy(v.GetName(), rpi, true); err != nil {
			return err
		}
	}

	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyDropDatabaseCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DropDatabaseCommand_Command)
	v := ext.(*internal.DropDatabaseCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.DropDatabase(v.GetName()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyCreateRetentionPolicyCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateRetentionPolicyCommand_Command)
	v := ext.(*internal.CreateRetentionPolicyCommand)
	rpi := meta.RetentionPolicyInfo{}
	_ = rpi.UnmarshalBinary(v.GetRetentionPolicy())

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.CreateRetentionPolicy(v.GetDatabase(), &rpi, false); err != nil {
		return err
	}

	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyDropRetentionPolicyCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DropRetentionPolicyCommand_Command)
	v := ext.(*internal.DropRetentionPolicyCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.DropRetentionPolicy(v.GetDatabase(), v.GetName()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applySetDefaultRetentionPolicyCommand(cmd *internal.Command) interface{} {
	// ext, _ := proto.GetExtension(cmd, internal.E_SetDefaultRetentionPolicyCommand_Command)
	// v := ext.(*internal.SetDefaultRetentionPolicyCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	// if err := other.Data.SetDefaultRetentionPolicY(v.GetDatabase(), v.GetName()); err != nil {
	// return err
	// }
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyUpdateRetentionPolicyCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_UpdateRetentionPolicyCommand_Command)
	v := ext.(*internal.UpdateRetentionPolicyCommand)

	// Create update object.
	rpu := meta.RetentionPolicyUpdate{Name: v.NewName}
	if v.Duration != nil {
		value := time.Duration(v.GetDuration())
		rpu.Duration = &value
	}
	if v.ReplicaN != nil {
		value := int(v.GetReplicaN())
		rpu.ReplicaN = &value
	}

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.UpdateRetentionPolicy(v.GetDatabase(), v.GetName(), &rpu, false); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyCreateShardGroupCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateShardGroupCommand_Command)
	v := ext.(*internal.CreateShardGroupCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	//NOTE: here we override original CreateShardGroup. It has to call directly from data instead of data.Data
	if err := other.CreateShardGroup(v.GetDatabase(), v.GetPolicy(), time.Unix(0, v.GetTimestamp())); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyDeleteShardGroupCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DeleteShardGroupCommand_Command)
	v := ext.(*internal.DeleteShardGroupCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.DeleteShardGroup(v.GetDatabase(), v.GetPolicy(), v.GetShardGroupID()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyCreateContinuousQueryCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateContinuousQueryCommand_Command)
	v := ext.(*internal.CreateContinuousQueryCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.CreateContinuousQuery(v.GetDatabase(), v.GetName(), v.GetQuery()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyDropContinuousQueryCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DropContinuousQueryCommand_Command)
	v := ext.(*internal.DropContinuousQueryCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.DropContinuousQuery(v.GetDatabase(), v.GetName()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyCreateSubscriptionCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateSubscriptionCommand_Command)
	v := ext.(*internal.CreateSubscriptionCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.CreateSubscription(v.GetDatabase(), v.GetRetentionPolicy(), v.GetName(), v.GetMode(), v.GetDestinations()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyDropSubscriptionCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DropSubscriptionCommand_Command)
	v := ext.(*internal.DropSubscriptionCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.DropSubscription(v.GetDatabase(), v.GetRetentionPolicy(), v.GetName()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyCreateUserCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateUserCommand_Command)
	v := ext.(*internal.CreateUserCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.CreateUser(v.GetName(), v.GetHash(), v.GetAdmin()); err != nil {
		return err
	}
	fsm.data = other

	return nil
}

func (fsm *storeFSM) applyDropUserCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DropUserCommand_Command)
	v := ext.(*internal.DropUserCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.DropUser(v.GetName()); err != nil {
		return err
	}
	fsm.data = other
	return nil
}

func (fsm *storeFSM) applyUpdateUserCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_UpdateUserCommand_Command)
	v := ext.(*internal.UpdateUserCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.UpdateUser(v.GetName(), v.GetHash()); err != nil {
		return err
	}
	fsm.data = other
	return nil
}

func (fsm *storeFSM) applySetPrivilegeCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_SetPrivilegeCommand_Command)
	v := ext.(*internal.SetPrivilegeCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.SetPrivilege(v.GetUsername(), v.GetDatabase(), influxql.Privilege(v.GetPrivilege())); err != nil {
		return err
	}
	fsm.data = other
	return nil
}

func (fsm *storeFSM) applySetAdminPrivilegeCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_SetAdminPrivilegeCommand_Command)
	v := ext.(*internal.SetAdminPrivilegeCommand)

	// Copy data and update.
	other := fsm.data.Clone()
	if err := other.Data.SetAdminPrivilege(v.GetUsername(), v.GetAdmin()); err != nil {
		return err
	}
	fsm.data = other
	return nil
}

func (fsm *storeFSM) applySetDataCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_SetDataCommand_Command)
	v := ext.(*internal.SetDataCommand)

	// Overwrite data.
	fsm.data = &Data{}
	_ = fsm.data.UnmarshalBinary(v.GetData())
	return nil
}

func (fsm *storeFSM) applyCreateMetaNodeCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateMetaNodeCommand_Command)
	v := ext.(*internal.CreateMetaNodeCommand)

	other := fsm.data.Clone()
	_ = other.CreateMetaNode(v.GetHTTPAddr(), v.GetTCPAddr())

	// If the cluster ID hasn't been set then use the command's random number.
	if other.Data.ClusterID == 0 {
		other.Data.ClusterID = uint64(v.GetRand())
	}

	fsm.data = other
	return nil
}

func (fsm *storeFSM) applySetMetaNodeCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_SetMetaNodeCommand_Command)
	v := ext.(*internal.SetMetaNodeCommand)

	other := fsm.data.Clone()

	// If the cluster ID hasn't been set then use the command's random number.
	if other.Data.ClusterID == 0 {
		other.Data.ClusterID = uint64(v.GetRand())
	}

	_ = other.SetMetaNode(other.Data.ClusterID, v.GetHTTPAddr(), v.GetTCPAddr())

	fsm.data = other
	return nil
}

func (fsm *storeFSM) applyDeleteMetaNodeCommand(cmd *internal.Command, s *store) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DeleteMetaNodeCommand_Command)
	v := ext.(*internal.DeleteMetaNodeCommand)

	other := fsm.data.Clone()
	node := other.MetaNode(v.GetID())
	if node == nil {
		return ErrNodeNotFound
	}

	if err := s.leave(node); err != nil && err != raft.ErrNotLeader {
		return err
	}

	if err := other.DeleteMetaNode(v.GetID()); err != nil {
		return err
	}
	fsm.data = other
	return nil
}

func (fsm *storeFSM) applyCreateDataNodeCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_CreateDataNodeCommand_Command)
	v := ext.(*internal.CreateDataNodeCommand)

	other := fsm.data.Clone()
	_ = other.CreateDataNode(v.GetHTTPAddr(), v.GetTCPAddr())

	fsm.data = other
	return nil
}

func (fsm *storeFSM) applyDeleteDataNodeCommand(cmd *internal.Command) interface{} {
	ext, _ := proto.GetExtension(cmd, internal.E_DeleteDataNodeCommand_Command)
	v := ext.(*internal.DeleteDataNodeCommand)

	other := fsm.data.Clone()
	if err := other.DeleteDataNode(v.GetID()); err != nil {
		return err
	}
	fsm.data = other
	return nil
}

//TODO finish these functions
// func (fsm *storeFSM) applyUpdateDataNode(cmd *internal.Command) (interface{})            {}
// func (fsm *storeFSM) applyCreateDatabase(cmd *internal.Command) interface{} {}

// func (fsm *storeFSM) applyDropDatabase(cmd *internal.Command) (interface{})              {}
// func (fsm *storeFSM) applyCreateRetentionPolicy(cmd *internal.Command) (interface{})     {}
// func (fsm *storeFSM) applyDropRetentionPolicy(cmd *internal.Command) (interface{})       {}
// func (fsm *storeFSM) applySetDefaultRetentionPolicy(cmd *internal.Command) (interface{}) {}
// func (fsm *storeFSM) applyUpdateRetentionPolicy(cmd *internal.Command) (interface{})     {}
// func (fsm *storeFSM) applyDropShard(cmd *internal.Command) (interface{})                 {}
// func (fsm *storeFSM) applyCreateShardGroup(cmd *internal.Command) (interface{})          {}
// func (fsm *storeFSM) applyCreateBalancedShardGroup(cmd *internal.Command) (interface{})  {}
// func (fsm *storeFSM) applyDeleteShardGroup(cmd *internal.Command) (interface{})          {}
// func (fsm *storeFSM) applyTruncateShardGroups(cmd *internal.Command) (interface{})       {}
// func (fsm *storeFSM) applyAddShardOwner(cmd *internal.Command) (interface{})             {}
// func (fsm *storeFSM) applyRemoveShardOwner(cmd *internal.Command) (interface{})          {}
// func (fsm *storeFSM) applyCreateContinuousQuery(cmd *internal.Command) (interface{})     {}
// func (fsm *storeFSM) applyDropContinuousQuery(cmd *internal.Command) (interface{})       {}
// func (fsm *storeFSM) applyCreateSubscription(cmd *internal.Command) (interface{})        {}
// func (fsm *storeFSM) applyDropSubscription(cmd *internal.Command) (interface{})          {}
// func (fsm *storeFSM) applyCreateUser(cmd *internal.Command) (interface{})                {}
// func (fsm *storeFSM) applySetUserPassword(cmd *internal.Command) (interface{})           {}
// func (fsm *storeFSM) applyDropUser(cmd *internal.Command) (interface{})                  {}
// func (fsm *storeFSM) applyUpdateUser(cmd *internal.Command) (interface{})                {}
// func (fsm *storeFSM) applyAddUserPermissions(cmd *internal.Command) (interface{})        {}
// func (fsm *storeFSM) applyRemoveUserPermissions(cmd *internal.Command) (interface{})     {}
// func (fsm *storeFSM) applySetPrivilege(cmd *internal.Command) (interface{})              {}
// func (fsm *storeFSM) applySetAdminPrivilege(cmd *internal.Command) (interface{})         {}
// func (fsm *storeFSM) applySetData(cmd *internal.Command) (interface{})                   {}
// func (fsm *storeFSM) setData()                                                         {}
// func (fsm *storeFSM) applyImportData(cmd *internal.Command) (interface{})                {}
// func (fsm *storeFSM) applyCreateRole(cmd *internal.Command) (interface{})                {}
// func (fsm *storeFSM) applyDropRole(cmd *internal.Command) (interface{})                  {}
// func (fsm *storeFSM) applyAddRoleUsers(cmd *internal.Command) (interface{})              {}
// func (fsm *storeFSM) applyRemoveRoleUsers(cmd *internal.Command) (interface{})           {}
// func (fsm *storeFSM) applyAddRolePermissions(cmd *internal.Command) (interface{})        {}
// func (fsm *storeFSM) applyRemoveRolePermissions(cmd *internal.Command) (interface{})     {}
// func (fsm *storeFSM) applyChangeRoleName()                                             {}
// func (fsm *storeFSM) applyCreateMetaNode(cmd *internal.Command) (interface{})            {}
// func (fsm *storeFSM) applySetMetaNode(cmd *internal.Command) (interface{})               {}
// func (fsm *storeFSM) applyDeleteMetaNode(cmd *internal.Command) (interface{})            {}
// func (fsm *storeFSM) applyCreateDataNode(cmd *internal.Command) (interface{})            {}
// func (fsm *storeFSM) applyDeleteDataNode(cmd *internal.Command) (interface{})            {}
// func (fsm *storeFSM) applyRemovePendingShardOwner(cmd *internal.Command) (interface{})   {}
func (fsm *storeFSM) applyAddPendingShardOwner(cmd *internal.Command) interface{} {
	// ext, err := proto.GetExtension(cmd, internal.E_AddPendingShardOwnerCommand_Command)
	// if err != nil {
	// 	return nil
	// }
	// v := ext.(*internal.AddPendingShardOwnerCommand)

	return nil
}

// func (fsm *storeFSM) applyCommitPendingShardOwner(cmd *internal.Command) (interface{})   {}

func (fsm *storeFSM) Snapshot() (raft.FSMSnapshot, error) {
	s := (*store)(fsm)
	s.mu.Lock()
	defer s.mu.Unlock()

	return &storeFSMSnapshot{Data: (*store)(fsm).data}, nil
}

func (fsm *storeFSM) Restore(r io.ReadCloser) error {
	// Read all bytes.
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}

	// Decode metadata.
	data := &Data{}
	if err := data.UnmarshalBinary(b); err != nil {
		return err
	}

	// Set metadata on store.
	// NOTE: No lock because Hashicorp Raft doesn't call Restore concurrently
	// with any other function.
	fsm.data = data

	return nil
}

type storeFSMSnapshot struct {
	Data *Data
}

func (s *storeFSMSnapshot) Persist(sink raft.SnapshotSink) error {
	err := func() error {
		// Encode data.
		p, err := s.Data.MarshalBinary()
		if err != nil {
			return err
		}

		// Write data to sink.
		if _, err := sink.Write(p); err != nil {
			return err
		}

		// Close the sink.
		if err := sink.Close(); err != nil {
			return err
		}

		return nil
	}()

	if err != nil {
		_ = sink.Cancel()
		return err
	}

	return nil
}

// Release is invoked when we are finished with the snapshot
func (s *storeFSMSnapshot) Release() {}
