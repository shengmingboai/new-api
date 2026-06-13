package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

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
