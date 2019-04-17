package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"jiacrontab/models"
	"jiacrontab/pkg/proto"
	"jiacrontab/pkg/util"
	"strings"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/kataras/iris"
)

type CustomerClaims struct {
	jwt.StandardClaims
	UserID   uint
	Mail     string
	Username string
	GroupID  uint
	Root     bool
}

// Login 用户登录
func Login(c iris.Context) {
	var (
		err            error
		ctx            = wrapCtx(c)
		reqBody        LoginReqParams
		user           models.User
		customerClaims CustomerClaims
	)

	if err = ctx.Valid(&reqBody); err != nil {
		ctx.respParamError(err)
		return
	}
	if !user.Verify(reqBody.Username, reqBody.Passwd) {
		ctx.respAuthFailed(errors.New("帐号或密码不正确"))
		return
	}

	customerClaims.ExpiresAt = cfg.Jwt.Expires + time.Now().Unix()
	customerClaims.Username = reqBody.Username
	customerClaims.UserID = user.ID
	customerClaims.Mail = user.Mail
	customerClaims.GroupID = user.GroupID
	customerClaims.Root = user.Root

	if reqBody.Remember {
		customerClaims.ExpiresAt = time.Now().Add(24 * 30 * time.Hour).Unix()
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, customerClaims).SignedString([]byte(cfg.Jwt.SigningKey))

	if err != nil {
		ctx.respAuthFailed(errors.New("无法生成访问凭证"))
		return
	}

	ctx.respSucc("", map[string]interface{}{
		"token":   token,
		"groupID": user.GroupID,
		"root":    user.Root,
		"mail":    user.Mail,
		"userID":  user.ID,
	})
}

func GetActivityList(c iris.Context) {
	var (
		ctx     = wrapCtx(c)
		err     error
		reqBody ReadMoreReqParams
		events  []models.Event
	)

	if err = reqBody.verify(ctx); err != nil {
		ctx.respError(proto.Code_Error, err.Error(), nil)
		return
	}

	if err = ctx.parseClaimsFromToken(); err != nil {
		ctx.respBasicError(err)
		return
	}

	if reqBody.LastID == 0 {
		err = models.DB().Debug().Where("user_id=?", ctx.claims.UserID).
			Order(fmt.Sprintf("created_at %s", reqBody.Orderby)).
			Limit(reqBody.Pagesize).
			Find(&events).Error
	} else {
		err = models.DB().Debug().Where("user_id=? and id<?", ctx.claims.UserID, reqBody.LastID).
			Order(fmt.Sprintf("created_at %s", reqBody.Orderby)).
			Limit(reqBody.Pagesize).
			Find(&events).Error
	}

	if err != nil && err != sql.ErrNoRows {
		ctx.respDBError(err)
		return
	}

	ctx.respSucc("", map[string]interface{}{
		"list":     events,
		"pagesize": reqBody.Pagesize,
	})
}

func GetJobHistory(c iris.Context) {
	var (
		ctx      = wrapCtx(c)
		err      error
		reqBody  ReadMoreReqParams
		historys []models.JobHistory
		addrs    []string
	)

	if err = reqBody.verify(ctx); err != nil {
		ctx.respError(proto.Code_Error, err.Error(), nil)
		return
	}

	if addrs, err = ctx.getGroupAddr(); err != nil {
		ctx.respError(proto.Code_Error, err.Error(), err)
		return
	}

	if reqBody.LastID == 0 {
		err = models.DB().Debug().Where("addr in (?)", addrs).
			Order(fmt.Sprintf("created_at %s", reqBody.Orderby)).
			Limit(reqBody.Pagesize).
			Find(&historys).Error
	} else {
		err = models.DB().Debug().Where("addr in (?) and id<?", addrs, reqBody.LastID).
			Order(fmt.Sprintf("created_at %s", reqBody.Orderby)).
			Limit(reqBody.Pagesize).
			Find(&historys).Error
	}

	if err != nil && err != sql.ErrNoRows {
		ctx.respDBError(err)
		return
	}

	ctx.respSucc("", map[string]interface{}{
		"list":     historys,
		"pagesize": reqBody.Pagesize,
	})
}

func AuditJob(c iris.Context) {
	var (
		ctx     = wrapCtx(c)
		err     error
		reqBody AuditJobReqParams
	)

	if err = reqBody.verify(ctx); err != nil {
		ctx.respBasicError(err)
		return
	}

	if !ctx.verifyNodePermission(reqBody.Addr) {
		ctx.respNotAllowed()
		return
	}

	if ctx.claims.GroupID != models.SuperGroup.ID && !ctx.claims.Root {
		ctx.respNotAllowed()
		return
	}

	if reqBody.JobType == "crontab" {
		var reply []models.CrontabJob
		if err = rpcCall(reqBody.Addr, "CrontabJob.Audit", proto.AuditJobArgs{
			JobIDs: reqBody.JobIDs,
		}, &reply); err != nil {
			ctx.respRPCError(err)
			return
		}
		var targetNames []string
		for _, v := range reply {
			targetNames = append(targetNames, v.Name)
		}
		ctx.pubEvent(strings.Join(targetNames, ","), event_AuditCrontabJob, reqBody.Addr, reqBody)
	} else {
		var reply []models.DaemonJob
		if err = rpcCall(reqBody.Addr, "DaemonJob.Audit", proto.AuditJobArgs{
			JobIDs: reqBody.JobIDs,
		}, &reply); err != nil {
			ctx.respRPCError(err)
			return
		}
		var targetNames []string
		for _, v := range reply {
			targetNames = append(targetNames, v.Name)
		}
		ctx.pubEvent(strings.Join(targetNames, ","), event_AuditDaemonJob, reqBody.Addr, reqBody)
	}

	ctx.respSucc("", nil)
}

// IninAdminUser 初始化管理员
func IninAdminUser(c iris.Context) {
	var (
		err     error
		ctx     = wrapCtx(c)
		user    models.User
		reqBody UserReqParams
	)

	if err = ctx.Valid(&reqBody); err != nil {
		ctx.respParamError(err)
		return
	}

	if ret := models.DB().Debug().Take(&user, "group_id=?", 1); ret.Error == nil && ret.RowsAffected > 0 {
		ctx.respNotAllowed()
		return
	}

	user.Username = reqBody.Username
	user.Passwd = reqBody.Passwd
	user.Root = true
	user.GroupID = models.SuperGroup.ID
	user.Mail = reqBody.Mail

	if err = user.Create(); err != nil {
		ctx.respError(proto.Code_Error, err.Error(), nil)
		return
	}

	cfg.SetUsed()
	ctx.respSucc("", true)
}

// Signup 注册新用户
func Signup(c iris.Context) {
	var (
		err     error
		ctx     = wrapCtx(c)
		user    models.User
		reqBody UserReqParams
	)

	if err = ctx.Valid(&reqBody); err != nil {
		ctx.respParamError(err)
		return
	}

	if !ctx.isSuper() {
		ctx.respNotAllowed()
		return
	}

	user.Username = reqBody.Username
	user.Passwd = reqBody.Passwd
	user.GroupID = reqBody.GroupID
	user.Root = reqBody.Root
	user.Mail = reqBody.Mail

	if err = user.Create(); err != nil {
		ctx.respDBError(err)
		return
	}

	ctx.pubEvent(user.Username, event_SignUpUser, "", reqBody)
	ctx.respSucc("", true)
}

// UserStat 统计信息
func UserStat(c iris.Context) {
	var (
		err          error
		ctx          = wrapCtx(c)
		auditNumStat struct {
			CrontabJobAuditNum  uint
			DaemonJobAuditNum   uint
			CrontabJobFailNum   uint
			DaemonJobRunningNum uint
			NodeNum             uint
		}
	)

	if err = ctx.parseClaimsFromToken(); err != nil {
		ctx.respJWTError(err)
		return
	}

	err = models.DB().Raw(
		`select 
			sum(crontab_job_audit_num) as crontab_job_audit_num, 
			sum(daemon_job_audit_num) as daemon_job_audit_num,
			sum(crontab_job_fail_num) as crontab_job_fail_num,
			sum(daemon_job_running_num) as daemon_job_running_num,
			count(*) as node_num
		from nodes 
		where group_id=?`, ctx.claims.GroupID).Debug().Scan(&auditNumStat).Error
	if err != nil {
		ctx.respDBError(err)
		return
	}

	ctx.respSucc("", map[string]interface{}{
		"systemInfo": util.SystemInfo(cfg.ServerStartTime),
		"auditStat":  auditNumStat,
	})
}

// GroupUser 超级管理员设置普通用户分组
func GroupUser(c iris.Context) {
	var (
		ctx     = wrapCtx(c)
		reqBody SetGroupReqParams
		err     error
		user    models.User
		group   models.Group
	)

	if err = ctx.Valid(&reqBody); err != nil {
		ctx.respBasicError(err)
		return
	}

	if !ctx.isSuper() {
		ctx.respNotAllowed()
		return
	}

	if reqBody.TargetGroupName != "" {
		group.Name = reqBody.TargetGroupName
		if err = models.DB().Save(&group).Error; err != nil {
			ctx.respDBError(err)
			return
		}
		reqBody.TargetGroupID = group.ID
	}

	user.ID = reqBody.UserID
	user.GroupID = reqBody.TargetGroupID
	user.Root = reqBody.Root
	if err = user.SetGroup(); err != nil {
		ctx.respDBError(err)
		return
	}

	ctx.pubEvent(user.Username, event_GroupUser, "", reqBody)
	ctx.respSucc("", nil)
}

// GetUserList 获得用户列表
// 支持获得全部用户，所属分组用户，指定分组用户（超级管理员）
func GetUserList(c iris.Context) {
	var (
		ctx      = wrapCtx(c)
		reqBody  GetUsersParams
		userList []models.User
		err      error
		total    int
	)

	if err = ctx.Valid(&reqBody); err != nil {
		ctx.respParamError(err)
	}

	if err = ctx.parseClaimsFromToken(); err != nil {
		ctx.respJWTError(err)
		return
	}

	if reqBody.IsAll && ctx.claims.GroupID != models.SuperGroup.ID {
		ctx.respNotAllowed()
		return
	}

	if !reqBody.IsAll && reqBody.QueryGroupID != ctx.claims.GroupID && ctx.claims.GroupID != models.SuperGroup.ID {
		ctx.respNotAllowed()
		return
	}

	if reqBody.QueryGroupID == 0 {
		reqBody.QueryGroupID = ctx.claims.GroupID
	}

	m := models.DB().Model(&models.User{})
	if reqBody.IsAll {
		err = m.Count(&total).Error
	} else {
		err = m.Where("group_id=?", reqBody.QueryGroupID).Count(&total).Error
	}

	if err != nil && err != sql.ErrNoRows {
		ctx.respBasicError(err)
		return
	}

	if reqBody.IsAll {
		err = models.DB().Debug().Preload("Group").Limit(reqBody.Pagesize).Find(&userList).Error
	} else {
		err = models.DB().Debug().Preload("Group").Where("group_id=?", reqBody.QueryGroupID).Offset(reqBody.Page - 1).Limit(reqBody.Pagesize).Find(&userList).Error
	}

	if err != nil && err != sql.ErrNoRows {
		ctx.respDBError(err)
		return
	}

	ctx.respSucc("", map[string]interface{}{
		"list":     userList,
		"total":    total,
		"page":     reqBody.Page,
		"pagesize": reqBody.Pagesize,
	})
}