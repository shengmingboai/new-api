package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelTestLogDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.LogConsumeEnabled = originalLogConsumeEnabled
	})

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.LogConsumeEnabled = true

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func TestSettleTestQuotaUsesTieredBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:   "tiered_expr",
			ExprString:    `param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`,
			ExprHash:      billingexpr.ExprHashString(`param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`),
			GroupRatio:    1,
			EstimatedTier: "stream",
			QuotaPerUnit:  common.QuotaPerUnit,
			ExprVersion:   1,
		},
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{"stream":true}`),
		},
	}

	quota, result := settleTestQuota(info, types.PriceData{
		ModelRatio:      1,
		CompletionRatio: 2,
	}, &dto.Usage{
		PromptTokens: 1000,
	})

	require.Equal(t, 1500, quota)
	require.NotNil(t, result)
	require.Equal(t, "stream", result.MatchedTier)
}

func TestBuildTestLogOtherInjectsTieredInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode: "tiered_expr",
			ExprString:  `tier("base", p * 2)`,
		},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	priceData := types.PriceData{
		GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 12,
		},
	}

	other := buildTestLogOther(ctx, info, priceData, usage, &billingexpr.TieredResult{
		MatchedTier: "base",
	})

	require.Equal(t, "tiered_expr", other["billing_mode"])
	require.Equal(t, "base", other["matched_tier"])
	require.NotEmpty(t, other["expr_b64"])
}

func TestResolveChannelTestUserIDUsesRequestUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("id", 2)

	userID, err := resolveChannelTestUserID(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, userID)
}

func TestShouldTestChannelForScheduledTest(t *testing.T) {
	originalAutomaticDisable := common.AutomaticDisableChannelEnabled
	originalAutomaticEnable := common.AutomaticEnableChannelEnabled
	originalMonitorSetting := *operation_setting.GetMonitorSetting()
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalAutomaticDisable
		common.AutomaticEnableChannelEnabled = originalAutomaticEnable
		*operation_setting.GetMonitorSetting() = originalMonitorSetting
	})

	tests := []struct {
		name              string
		autoDisable       bool
		scheduledDisable  bool
		autoEnable        bool
		status            int
		expectedShouldRun bool
	}{
		{
			name:              "enabled channel is tested when scheduled disable is enabled",
			autoDisable:       true,
			scheduledDisable:  true,
			autoEnable:        false,
			status:            common.ChannelStatusEnabled,
			expectedShouldRun: true,
		},
		{
			name:              "enabled channel is skipped when scheduled disable is disabled",
			autoDisable:       true,
			scheduledDisable:  false,
			autoEnable:        true,
			status:            common.ChannelStatusEnabled,
			expectedShouldRun: false,
		},
		{
			name:              "auto disabled channel is tested when automatic enable is enabled",
			autoDisable:       false,
			scheduledDisable:  false,
			autoEnable:        true,
			status:            common.ChannelStatusAutoDisabled,
			expectedShouldRun: true,
		},
		{
			name:              "auto disabled channel is skipped when automatic enable is disabled",
			autoDisable:       true,
			scheduledDisable:  true,
			autoEnable:        false,
			status:            common.ChannelStatusAutoDisabled,
			expectedShouldRun: false,
		},
		{
			name:              "manual disabled channel is always skipped",
			autoDisable:       true,
			scheduledDisable:  true,
			autoEnable:        true,
			status:            common.ChannelStatusManuallyDisabled,
			expectedShouldRun: false,
		},
		{
			name:              "enabled channel is skipped when both scheduled switches are disabled",
			autoDisable:       true,
			scheduledDisable:  false,
			autoEnable:        false,
			status:            common.ChannelStatusEnabled,
			expectedShouldRun: false,
		},
		{
			name:              "enabled channel is skipped when global automatic disable is disabled",
			autoDisable:       false,
			scheduledDisable:  true,
			autoEnable:        false,
			status:            common.ChannelStatusEnabled,
			expectedShouldRun: false,
		},
		{
			name:              "auto disabled channel is skipped when both scheduled switches are disabled",
			autoDisable:       true,
			scheduledDisable:  false,
			autoEnable:        false,
			status:            common.ChannelStatusAutoDisabled,
			expectedShouldRun: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			common.AutomaticDisableChannelEnabled = test.autoDisable
			common.AutomaticEnableChannelEnabled = test.autoEnable
			operation_setting.GetMonitorSetting().AutoTestChannelDisableOnFailure = test.scheduledDisable

			channel := &model.Channel{Status: test.status}

			require.Equal(t, test.expectedShouldRun, shouldTestChannelForScheduledTest(channel))
		})
	}
}

func TestShouldTestChannelForManualTest(t *testing.T) {
	require.True(t, shouldTestChannelForManualTest(&model.Channel{Status: common.ChannelStatusEnabled}))
	require.True(t, shouldTestChannelForManualTest(&model.Channel{Status: common.ChannelStatusAutoDisabled}))
	require.False(t, shouldTestChannelForManualTest(&model.Channel{Status: common.ChannelStatusManuallyDisabled}))
}

func TestRecordFailedChannelTestLogRecordsModelTestFailure(t *testing.T) {
	db := setupChannelTestLogDB(t)
	require.NoError(t, db.Create(&model.User{
		Id:       1,
		Username: "root",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("username", "root")
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	channel := &model.Channel{
		Id:     7,
		Name:   "failed-test-channel",
		Type:   1,
		Status: common.ChannelStatusAutoDisabled,
	}
	apiErr := types.NewOpenAIError(errors.New("invalid api key"), types.ErrorCodeBadResponse, http.StatusUnauthorized)

	recordFailedChannelTestLog(ctx, channel, 1, "", "", false, time.Now(), testResult{
		localErr:    apiErr,
		newAPIError: apiErr,
	})

	var log model.Log
	require.NoError(t, db.Order("id desc").First(&log).Error)
	require.Equal(t, model.LogTypeConsume, log.Type)
	require.Equal(t, "模型测试", log.TokenName)
	require.Equal(t, "gpt-test", log.ModelName)
	require.Equal(t, 0, log.Quota)
	require.Equal(t, 7, log.ChannelId)
	require.Contains(t, log.Content, "模型测试失败")
	require.Contains(t, log.Content, "invalid api key")
	require.Contains(t, log.Other, `"test_success":false`)
}

func TestScheduledDisableChecksRequireGlobalAndScheduledSwitches(t *testing.T) {
	originalAutomaticDisable := common.AutomaticDisableChannelEnabled
	originalMonitorSetting := *operation_setting.GetMonitorSetting()
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalAutomaticDisable
		*operation_setting.GetMonitorSetting() = originalMonitorSetting
	})

	err := types.NewOpenAIError(errors.New("invalid api key"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)

	common.AutomaticDisableChannelEnabled = true
	operation_setting.GetMonitorSetting().AutoTestChannelDisableOnFailure = true
	require.True(t, shouldDisableChannelForScheduledTest(err))
	require.True(t, shouldApplyDisableThresholdForScheduledTest())

	common.AutomaticDisableChannelEnabled = false
	operation_setting.GetMonitorSetting().AutoTestChannelDisableOnFailure = true
	require.False(t, shouldDisableChannelForScheduledTest(err))
	require.False(t, shouldApplyDisableThresholdForScheduledTest())

	common.AutomaticDisableChannelEnabled = true
	operation_setting.GetMonitorSetting().AutoTestChannelDisableOnFailure = false
	require.False(t, shouldDisableChannelForScheduledTest(err))
	require.False(t, shouldApplyDisableThresholdForScheduledTest())
}
