package bitfinex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thrasher-corp/gocryptotrader/common"
	"github.com/thrasher-corp/gocryptotrader/common/convert"
	"github.com/thrasher-corp/gocryptotrader/common/crypto"
	"github.com/thrasher-corp/gocryptotrader/currency"
	exchange "github.com/thrasher-corp/gocryptotrader/exchanges"
	"github.com/thrasher-corp/gocryptotrader/exchanges/asset"
	"github.com/thrasher-corp/gocryptotrader/exchanges/order"
	"github.com/thrasher-corp/gocryptotrader/exchanges/request"
	"github.com/thrasher-corp/gocryptotrader/portfolio/withdraw"
)

const (
	bitfinexAPIURLBase = "https://api.bitfinex.com"
	// Version 1 API endpoints
	bitfinexAPIVersion         = "/v1/"
	bitfinexStats              = "stats/"
	bitfinexAccountInfo        = "account_infos"
	bitfinexAccountFees        = "account_fees"
	bitfinexAccountSummary     = "summary"
	bitfinexBalances           = "balances"
	bitfinexTransfer           = "transfer"
	bitfinexWithdrawal         = "withdraw"
	bitfinexOrderNew           = "order/new"
	bitfinexOrderNewMulti      = "order/new/multi"
	bitfinexOrderCancel        = "order/cancel"
	bitfinexOrderCancelMulti   = "order/cancel/multi"
	bitfinexOrderCancelAll     = "order/cancel/all"
	bitfinexOrderCancelReplace = "order/cancel/replace"
	bitfinexOrderStatus        = "order/status"
	bitfinexInactiveOrders     = "hist"
	bitfinexOrders             = "orders"
	bitfinexPositions          = "positions"
	bitfinexClaimPosition      = "position/claim"
	bitfinexHistory            = "history"
	bitfinexHistoryMovements   = "movements"
	bitfinexTradeHistory       = "mytrades"
	bitfinexOfferNew           = "offer/new"
	bitfinexOfferCancel        = "offer/cancel"
	bitfinexActiveCredits      = "credits"
	bitfinexOffers             = "offers"
	bitfinexMarginActiveFunds  = "taken_funds"
	bitfinexMarginUnusedFunds  = "unused_taken_funds"
	bitfinexMarginTotalFunds   = "total_taken_funds"
	bitfinexMarginClose        = "funding/close"
	bitfinexLendbook           = "lendbook/"
	bitfinexLends              = "lends/"
	bitfinexLeaderboard        = "rankings"

	// Version 2 API endpoints
	bitfinexAPIVersion2     = "/v2/"
	bitfinexV2MarginFunding = "calc/trade/avg?"
	bitfinexV2Balances      = "auth/r/wallets"
	bitfinexV2AccountInfo   = "auth/r/info/user"
	bitfinexV2MarginInfo    = "auth/r/info/margin/"
	bitfinexV2FundingInfo   = "auth/r/info/funding/%s"
	bitfinexV2Auth          = "auth/"
	bitfinexDerivativeData  = "status/deriv?"
	bitfinexPlatformStatus  = "platform/status"
	bitfinexTickerBatch     = "tickers"
	bitfinexTicker          = "ticker/"
	bitfinexTrades          = "trades/"
	bitfinexOrderbook       = "book/"
	bitfinexHistoryShort    = "hist"
	bitfinexCandles         = "candles/trade"
	bitfinexKeyPermissions  = "key_info"
	bitfinexMarginInfo      = "margin_infos"
	bitfinexDepositMethod   = "conf/pub:map:tx:method"
	bitfinexDepositAddress  = "auth/w/deposit/address"
	bitfinexOrderUpdate     = "auth/w/order/update"

	bitfinexMarginPairs        = "conf/pub:list:pair:margin"
	bitfinexSpotPairs          = "conf/pub:list:pair:exchange"
	bitfinexMarginFundingPairs = "conf/pub:list:currency"
	bitfinexFuturesPairs       = "conf/pub:list:pair:futures"    // TODO: Implement
	bitfinexSecuritiesPairs    = "conf/pub:list:pair:securities" // TODO: Implement

	bitfinexInfoPairs       = "conf/pub:info:pair"
	bitfinexInfoFuturePairs = "conf/pub:info:pair:futures"

	// Bitfinex platform status values
	// When the platform is marked in maintenance mode bots should stop trading
	// activity. Cancelling orders will be possible.
	bitfinexMaintenanceMode = 0
	bitfinexOperativeMode   = 1

	bitfinexChecksumFlag   = 131072
	bitfinexWsSequenceFlag = 65536
)

// Bitfinex is the overarching type across the bitfinex package
type Bitfinex struct {
	exchange.Base
	WebsocketSubdChannels map[int]WebsocketChanInfo
}

// GetPlatformStatus returns the Bifinex platform status
func (b *Bitfinex) GetPlatformStatus(ctx context.Context) (int, error) {
	var response []int
	err := b.SendHTTPRequest(ctx, exchange.RestSpot,
		bitfinexAPIVersion2+
			bitfinexPlatformStatus,
		&response,
		platformStatus)
	if err != nil {
		return -1, err
	}

	switch response[0] {
	case bitfinexOperativeMode:
		return bitfinexOperativeMode, nil
	case bitfinexMaintenanceMode:
		return bitfinexMaintenanceMode, nil
	}

	return -1, fmt.Errorf("unexpected platform status value %d", response[0])
}

func baseMarginInfo(data []interface{}) (MarginInfoV2, error) {
	var resp MarginInfoV2
	tempData, ok := data[1].([]interface{})
	if !ok {
		return resp, fmt.Errorf("%w", errTypeAssert)
	}
	resp.UserPNL, ok = tempData[0].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for UserPNL", errTypeAssert)
	}
	resp.UserSwaps, ok = tempData[1].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for UserSwaps", errTypeAssert)
	}
	resp.MarginBalance, ok = tempData[2].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for MarginBalance", errTypeAssert)
	}
	resp.MarginNet, ok = tempData[3].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for MarginNet", errTypeAssert)
	}
	resp.MarginMin, ok = tempData[4].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for MarginMin", errTypeAssert)
	}
	return resp, nil
}

func symbolMarginInfo(data []interface{}) ([]MarginInfoV2, error) {
	resp := make([]MarginInfoV2, len(data))
	for x := range data {
		var tempResp MarginInfoV2
		tempData, ok := data[x].([]interface{})
		if !ok {
			return nil, fmt.Errorf("%w for all sym", errTypeAssert)
		}
		var check bool
		tempResp.Symbol, check = tempData[1].(string)
		if !check {
			return nil, fmt.Errorf("%w for symbol data", errTypeAssert)
		}
		tempFloatData, check := tempData[2].([]interface{})
		if !check {
			return nil, fmt.Errorf("%w for symbol data", errTypeAssert)
		}
		if len(tempFloatData) < 4 {
			return nil, errors.New("invalid data received")
		}
		tempResp.TradableBalance, ok = tempFloatData[0].(float64)
		if !ok {
			return nil, fmt.Errorf("%w for TradableBalance", errTypeAssert)
		}
		tempResp.GrossBalance, ok = tempFloatData[1].(float64)
		if !ok {
			return nil, fmt.Errorf("%w for GrossBalance", errTypeAssert)
		}
		tempResp.BestAskAmount, ok = tempFloatData[2].(float64)
		if !ok {
			return nil, fmt.Errorf("%w for BestAskAmount", errTypeAssert)
		}
		tempResp.BestBidAmount, ok = tempFloatData[3].(float64)
		if !ok {
			return nil, fmt.Errorf("%w for BestBidAmount", errTypeAssert)
		}
		resp[x] = tempResp
	}
	return resp, nil
}

func defaultMarginV2Info(data []interface{}) (MarginInfoV2, error) {
	var resp MarginInfoV2
	var ok bool
	resp.Symbol, ok = data[1].(string)
	if !ok {
		return resp, fmt.Errorf("%w for symbol", errTypeAssert)
	}
	tempData, check := data[2].([]interface{})
	if !check {
		return resp, fmt.Errorf("%w for symbol data", errTypeAssert)
	}
	if len(tempData) < 4 {
		return resp, errors.New("invalid data received")
	}
	resp.TradableBalance, ok = tempData[0].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for TradableBalance", errTypeAssert)
	}
	resp.GrossBalance, ok = tempData[1].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for GrossBalance", errTypeAssert)
	}
	resp.BestAskAmount, ok = tempData[2].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for BestAskAmount", errTypeAssert)
	}
	resp.BestBidAmount, ok = tempData[3].(float64)
	if !ok {
		return resp, fmt.Errorf("%w for BestBidAmount", errTypeAssert)
	}
	return resp, nil
}

// GetV2MarginInfo gets v2 margin info for a symbol provided
// symbol: base, sym_all, any other trading symbol example tBTCUSD
func (b *Bitfinex) GetV2MarginInfo(ctx context.Context, symbol string) ([]MarginInfoV2, error) {
	var data []interface{}
	err := b.SendAuthenticatedHTTPRequestV2(ctx,
		exchange.RestSpot, http.MethodPost,
		bitfinexV2MarginInfo+symbol,
		nil,
		&data,
		getMarginInfoRate)
	if err != nil {
		return nil, err
	}
	var tempResp MarginInfoV2
	switch symbol {
	case "base":
		tempResp, err = baseMarginInfo(data)
		if err != nil {
			return nil, fmt.Errorf("%v - %s: %w", b.Name, symbol, err)
		}
	case "sym_all":
		var resp []MarginInfoV2
		resp, err = symbolMarginInfo(data)
		return resp, err
	default:
		tempResp, err = defaultMarginV2Info(data)
		if err != nil {
			return nil, fmt.Errorf("%v - %s: %w", b.Name, symbol, err)
		}
	}
	return []MarginInfoV2{tempResp}, nil
}

// GetV2MarginFunding gets borrowing rates for margin trading
func (b *Bitfinex) GetV2MarginFunding(ctx context.Context, symbol, amount string, period int32) (MarginV2FundingData, error) {
	var resp []interface{}
	var response MarginV2FundingData
	params := make(map[string]interface{})
	params["symbol"] = symbol
	params["period"] = period
	params["amount"] = amount
	err := b.SendAuthenticatedHTTPRequestV2(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexV2MarginFunding,
		params,
		&resp,
		getMarginInfoRate)
	if err != nil {
		return response, err
	}
	if len(resp) != 2 {
		return response, errors.New("invalid data received")
	}
	avgRate, ok := resp[0].(float64)
	if !ok {
		return response, fmt.Errorf("%v - %v: %w for rate", b.Name, symbol, errTypeAssert)
	}
	avgAmount, ok := resp[1].(float64)
	if !ok {
		return response, fmt.Errorf("%v - %v: %w for amount", b.Name, symbol, errTypeAssert)
	}
	response.Symbol = symbol
	response.RateAverage = avgRate
	response.AmountAverage = avgAmount
	return response, nil
}

// GetV2FundingInfo gets funding info for margin pairs
func (b *Bitfinex) GetV2FundingInfo(ctx context.Context, key string) (MarginFundingDataV2, error) {
	var resp []interface{}
	var response MarginFundingDataV2
	err := b.SendAuthenticatedHTTPRequestV2(ctx, exchange.RestSpot, http.MethodPost,
		fmt.Sprintf(bitfinexV2FundingInfo, key),
		nil,
		&resp,
		getAccountFees)
	if err != nil {
		return response, err
	}
	if len(resp) != 3 {
		return response, errors.New("invalid data received")
	}
	sym, ok := resp[0].(string)
	if !ok {
		return response, fmt.Errorf("%v GetV2FundingInfo: %w for sym", b.Name, errTypeAssert)
	}
	symbol, ok := resp[1].(string)
	if !ok {
		return response, fmt.Errorf("%v GetV2FundingInfo: %w for symbol", b.Name, errTypeAssert)
	}
	fundingData, ok := resp[2].([]interface{})
	if !ok {
		return response, fmt.Errorf("%v GetV2FundingInfo: %w for fundingData", b.Name, errTypeAssert)
	}
	response.Sym = sym
	response.Symbol = symbol
	if len(fundingData) < 4 {
		return response, fmt.Errorf("%v GetV2FundingInfo: invalid length of fundingData", b.Name)
	}
	if response.Data.YieldLoan, ok = fundingData[0].(float64); !ok {
		return response, errors.New("type conversion failed for YieldLoan")
	}
	if response.Data.YieldLend, ok = fundingData[1].(float64); !ok {
		return response, errors.New("type conversion failed for YieldLend")
	}
	if response.Data.DurationLoan, ok = fundingData[2].(float64); !ok {
		return response, errors.New("type conversion failed for DurationLoan")
	}
	if response.Data.DurationLend, ok = fundingData[3].(float64); !ok {
		return response, errors.New("type conversion failed for DurationLend")
	}
	return response, nil
}

// GetAccountInfoV2 gets V2 account data
func (b *Bitfinex) GetAccountInfoV2(ctx context.Context) (AccountV2Data, error) {
	var resp AccountV2Data
	var data []interface{}
	err := b.SendAuthenticatedHTTPRequestV2(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexV2AccountInfo,
		nil,
		&data,
		getAccountFees)
	if err != nil {
		return resp, err
	}
	if len(data) < 8 {
		return resp, fmt.Errorf("%v GetAccountInfoV2: invalid length of data", b.Name)
	}
	var ok bool
	var tempString string
	var tempFloat float64
	if tempFloat, ok = data[0].(float64); !ok {
		return resp, fmt.Errorf("%v GetAccountInfoV2: %w for id", b.Name, errTypeAssert)
	}
	resp.ID = int64(tempFloat)
	if tempString, ok = data[1].(string); !ok {
		return resp, fmt.Errorf("%v GetAccountInfoV2: %w for email", b.Name, errTypeAssert)
	}
	resp.Email = tempString
	if tempString, ok = data[2].(string); !ok {
		return resp, fmt.Errorf("%v GetAccountInfoV2: %w for username", b.Name, errTypeAssert)
	}
	resp.Username = tempString
	if tempFloat, ok = data[3].(float64); !ok {
		return resp, fmt.Errorf("%v GetAccountInfoV2: %w for accountcreate", b.Name, errTypeAssert)
	}
	resp.MTSAccountCreate = int64(tempFloat)
	if tempFloat, ok = data[4].(float64); !ok {
		return resp, fmt.Errorf("%v GetAccountInfoV2: %w failed for verified", b.Name, errTypeAssert)
	}
	resp.Verified = int64(tempFloat)
	if tempString, ok = data[7].(string); !ok {
		return resp, fmt.Errorf("%v GetAccountInfoV2: %w for timezone", b.Name, errTypeAssert)
	}
	resp.Timezone = tempString
	return resp, nil
}

// GetV2Balances gets v2 balances
func (b *Bitfinex) GetV2Balances(ctx context.Context) ([]WalletDataV2, error) {
	var data [][4]interface{}
	err := b.SendAuthenticatedHTTPRequestV2(ctx,
		exchange.RestSpot, http.MethodPost,
		bitfinexV2Balances,
		nil,
		&data,
		getAccountFees)
	if err != nil {
		return nil, err
	}
	resp := make([]WalletDataV2, len(data))
	for x := range data {
		wType, ok := data[x][0].(string)
		if !ok {
			return resp, fmt.Errorf("%v GetV2Balances: %w for walletType", b.Name, errTypeAssert)
		}
		curr, ok := data[x][1].(string)
		if !ok {
			return resp, fmt.Errorf("%v GetV2Balances: %w for currency", b.Name, errTypeAssert)
		}
		bal, ok := data[x][2].(float64)
		if !ok {
			return resp, fmt.Errorf("%v GetV2Balances: %w for balance", b.Name, errTypeAssert)
		}
		unsettledInterest, ok := data[x][3].(float64)
		if !ok {
			return resp, fmt.Errorf("%v GetV2Balances: %w for unsettledInterest", b.Name, errTypeAssert)
		}
		resp[x] = WalletDataV2{
			WalletType:        wType,
			Currency:          curr,
			Balance:           bal,
			UnsettledInterest: unsettledInterest,
		}
	}
	return resp, nil
}

// GetPairs gets pairs for different assets
func (b *Bitfinex) GetPairs(ctx context.Context, a asset.Item) ([]string, error) {
	switch a {
	case asset.Spot:
		list, err := b.GetSiteListConfigData(ctx, bitfinexSpotPairs)
		if err != nil {
			return nil, err
		}
		filter, err := b.GetSiteListConfigData(ctx, bitfinexSecuritiesPairs)
		if err != nil {
			return nil, err
		}
		filtered := make([]string, 0, len(list))
		for x := range list {
			if common.StringDataCompare(filter, list[x]) {
				continue
			}
			filtered = append(filtered, list[x])
		}
		return filtered, nil
	case asset.Margin:
		return b.GetSiteListConfigData(ctx, bitfinexMarginPairs)
	case asset.Futures:
		return b.GetSiteListConfigData(ctx, bitfinexFuturesPairs)
	case asset.MarginFunding:
		funding, err := b.GetTickerBatch(ctx)
		if err != nil {
			return nil, err
		}
		var pairs []string
		for key := range funding {
			symbol := key[1:]
			if key[0] != 'f' || strings.Contains(symbol, ":") || len(symbol) > 6 {
				continue
			}
			pairs = append(pairs, symbol)
		}
		return pairs, nil
	default:
		return nil, fmt.Errorf("%v GetPairs: %v %w", b.Name, a, asset.ErrNotSupported)
	}
}

// GetSiteListConfigData returns site configuration data by pub:list:{Object}:{Detail}
// string sets.
// NOTE: See https://docs.bitfinex.com/reference/rest-public-conf
func (b *Bitfinex) GetSiteListConfigData(ctx context.Context, set string) ([]string, error) {
	if set == "" {
		return nil, errSetCannotBeEmpty
	}

	var resp [][]string
	path := bitfinexAPIVersion2 + set
	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &resp, status)
	if err != nil {
		return nil, err
	}
	if len(resp) != 1 {
		return nil, errors.New("invalid response")
	}
	return resp[0], nil
}

// GetSiteInfoConfigData returns site configuration data by pub:info:{AssetType} as a map
// path should be bitfinexInfoPairs or bitfinexInfoPairsFuture???
// NOTE: See https://docs.bitfinex.com/reference/rest-public-conf
func (b *Bitfinex) GetSiteInfoConfigData(ctx context.Context, assetType asset.Item) ([]order.MinMaxLevel, error) {
	var path string
	switch assetType {
	case asset.Spot:
		path = bitfinexInfoPairs
	case asset.Futures:
		path = bitfinexInfoFuturePairs
	default:
		return nil, fmt.Errorf("invalid asset type for GetSiteInfoConfigData: %s", assetType)
	}
	url := bitfinexAPIVersion2 + path
	var resp [][][]any

	err := b.SendHTTPRequest(ctx, exchange.RestSpot, url, &resp, status)
	if err != nil {
		return nil, err
	}
	if len(resp) != 1 {
		return nil, errors.New("response did not contain only one item")
	}
	data := resp[0]
	pairs := make([]order.MinMaxLevel, 0, len(data))
	for i := range data {
		if len(data[i]) != 2 {
			return nil, errors.New("response contained a tuple without exactly 2 items")
		}
		pairSymbol, ok := data[i][0].(string)
		if !ok {
			return nil, fmt.Errorf("could not convert first item in SiteInfoConfigData to string: Type is %T", data[i][0])
		}
		if strings.Contains(pairSymbol, "TEST") {
			continue
		}
		// SIC: Array type really is any. It contains nils and strings
		info, ok := data[i][1].([]any)
		if !ok {
			return nil, fmt.Errorf("could not convert second item in SiteInfoConfigData to []any; Type is %T", data[i][1])
		}
		if len(info) < 5 {
			return nil, errors.New("response contained order info with less than 5 elements")
		}
		minOrder, err := convert.FloatFromString(info[3])
		if err != nil {
			return nil, fmt.Errorf("could not convert MinOrderAmount: %s", err)
		}
		maxOrder, err := convert.FloatFromString(info[4])
		if err != nil {
			return nil, fmt.Errorf("could not convert MaxOrderAmount: %s", err)
		}
		pair, err := currency.NewPairFromString(pairSymbol)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, order.MinMaxLevel{
			Asset:             assetType,
			Pair:              pair,
			MinimumBaseAmount: minOrder,
			MaximumBaseAmount: maxOrder,
		})
	}
	return pairs, nil
}

// GetDerivativeStatusInfo gets status data for the queried derivative
func (b *Bitfinex) GetDerivativeStatusInfo(ctx context.Context, keys, startTime, endTime string, sort, limit int64) ([]DerivativeDataResponse, error) {
	params := url.Values{}
	params.Set("keys", keys)
	if startTime != "" {
		params.Set("start", startTime)
	}
	if endTime != "" {
		params.Set("end", endTime)
	}
	if sort != 0 {
		params.Set("sort", strconv.FormatInt(sort, 10))
	}
	if limit != 0 {
		params.Set("limit", strconv.FormatInt(limit, 10))
	}

	var result [][]interface{}
	path := bitfinexAPIVersion2 + bitfinexDerivativeData +
		params.Encode()
	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &result, status)
	if err != nil {
		return nil, err
	}
	finalResp := make([]DerivativeDataResponse, len(result))
	for z := range result {
		if len(result[z]) < 19 {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: invalid response, array length too small, check api docs for updates", b.Name)
		}
		var response DerivativeDataResponse
		var ok bool
		if response.Key, ok = result[z][0].(string); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for Key", b.Name, errTypeAssert)
		}
		if response.MTS, ok = result[z][1].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for MTS", b.Name, errTypeAssert)
		}
		if response.DerivPrice, ok = result[z][3].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for DerivPrice", b.Name, errTypeAssert)
		}
		if response.SpotPrice, ok = result[z][4].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for SpotPrice", b.Name, errTypeAssert)
		}
		if response.InsuranceFundBalance, ok = result[z][6].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for Insurance fund balance", b.Name, errTypeAssert)
		}
		if response.NextFundingEventTS, ok = result[z][8].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for NextFundingEventTS", b.Name, errTypeAssert)
		}
		if response.NextFundingAccrued, ok = result[z][9].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for NextFundingAccrued", b.Name, errTypeAssert)
		}
		if response.NextFundingStep, ok = result[z][10].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for NextFundingStep", b.Name, errTypeAssert)
		}
		if response.CurrentFunding, ok = result[z][12].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for CurrentFunding", b.Name, errTypeAssert)
		}
		if response.MarkPrice, ok = result[z][15].(float64); !ok {
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for MarkPrice", b.Name, errTypeAssert)
		}

		switch t := result[z][18].(type) {
		case float64:
			response.OpenInterest = t
		case nil:
			break // OpenInterest will default to 0
		default:
			return finalResp, fmt.Errorf("%v GetDerivativeStatusInfo: %w for OpenInterest. Type received: %v",
				b.Name,
				errTypeAssert,
				t,
			)
		}
		finalResp[z] = response
	}
	return finalResp, nil
}

// GetTickerBatch returns all supported ticker information
func (b *Bitfinex) GetTickerBatch(ctx context.Context) (map[string]Ticker, error) {
	var response [][]interface{}

	path := bitfinexAPIVersion2 + bitfinexTickerBatch +
		"?symbols=ALL"

	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, tickerBatch)
	if err != nil {
		return nil, err
	}

	var tickers = make(map[string]Ticker)
	for x := range response {
		symbol, ok := response[x][0].(string)
		if !ok {
			return nil, common.GetTypeAssertError("string", response[x][0], "symbol")
		}

		var t Ticker
		if len(response[x]) > 11 {
			if t.FlashReturnRate, ok = response[x][1].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][1], "FlashReturnRate")
			}
			if t.Bid, ok = response[x][2].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][2], "bid")
			}
			var bidPeriod float64
			bidPeriod, ok = response[x][3].(float64)
			if !ok {
				return nil, common.GetTypeAssertError("float64", response[x][3], "bidPeriod")
			}
			t.BidPeriod = int64(bidPeriod)
			if t.BidSize, ok = response[x][4].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][4], "bidSize")
			}
			if t.Ask, ok = response[x][5].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][5], "ask")
			}
			var askPeriod float64
			askPeriod, ok = response[x][6].(float64)
			if !ok {
				return nil, common.GetTypeAssertError("float64", response[x][6], "askPeriod")
			}
			t.AskPeriod = int64(askPeriod)
			if t.AskSize, ok = response[x][7].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][7], "askSize")
			}
			if t.DailyChange, ok = response[x][8].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][8], "dailyChange")
			}
			if t.DailyChangePerc, ok = response[x][9].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][9], "dailyChangePerc")
			}
			if t.Last, ok = response[x][10].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][10], "last")
			}
			if t.Volume, ok = response[x][11].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][11], "volume")
			}
			if t.High, ok = response[x][12].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][12], "high")
			}
			if t.Low, ok = response[x][13].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][13], "low")
			}
			if t.FFRAmountAvailable, ok = response[x][16].(float64); !ok {
				return nil, common.GetTypeAssertError("float64", response[x][16], "FFRAmountAvailable")
			}

			tickers[symbol] = t
			continue
		}

		if t.Bid, ok = response[x][1].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][1], "bid")
		}
		if t.BidSize, ok = response[x][2].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][2], "bid size")
		}
		if t.Ask, ok = response[x][3].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][3], "ask")
		}
		if t.AskSize, ok = response[x][4].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][4], "ask size")
		}
		if t.DailyChange, ok = response[x][5].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][5], "daily change")
		}
		if t.DailyChangePerc, ok = response[x][6].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][6], "daily change percent")
		}
		if t.Last, ok = response[x][7].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][7], "last")
		}
		if t.Volume, ok = response[x][8].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][8], "volume")
		}
		if t.High, ok = response[x][9].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][9], "high")
		}
		if t.Low, ok = response[x][10].(float64); !ok {
			return nil, common.GetTypeAssertError("float64", response[x][10], "low")
		}
		tickers[symbol] = t
	}
	return tickers, nil
}

// GetTicker returns ticker information for one symbol
func (b *Bitfinex) GetTicker(ctx context.Context, symbol string) (*Ticker, error) {
	var response []interface{}

	path := bitfinexAPIVersion2 + bitfinexTicker + symbol

	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, tickerFunction)
	if err != nil {
		return nil, err
	}

	var t Ticker
	if len(response) > 10 {
		var ok bool
		if t.FlashReturnRate, ok = response[0].(float64); !ok {
			return nil, errors.New("unable to type assert flashReturnRate")
		}
		if t.Bid, ok = response[1].(float64); !ok {
			return nil, errors.New("unable to type assert bid")
		}
		var bidPeriod float64
		bidPeriod, ok = response[2].(float64)
		if !ok {
			return nil, errors.New("unable to type assert bidPeriod")
		}
		t.BidPeriod = int64(bidPeriod)
		if t.BidSize, ok = response[3].(float64); !ok {
			return nil, errors.New("unable to type assert bidSize")
		}
		if t.Ask, ok = response[4].(float64); !ok {
			return nil, errors.New("unable to type assert ask")
		}
		var askPeriod float64
		askPeriod, ok = response[5].(float64)
		if !ok {
			return nil, errors.New("unable to type assert askPeriod")
		}
		t.AskPeriod = int64(askPeriod)
		if t.AskSize, ok = response[6].(float64); !ok {
			return nil, errors.New("unable to type assert askSize")
		}
		if t.DailyChange, ok = response[7].(float64); !ok {
			return nil, errors.New("unable to type assert dailyChange")
		}
		if t.DailyChangePerc, ok = response[8].(float64); !ok {
			return nil, errors.New("unable to type assert dailyChangePerc")
		}
		if t.Last, ok = response[9].(float64); !ok {
			return nil, errors.New("unable to type assert last")
		}
		if t.Volume, ok = response[10].(float64); !ok {
			return nil, errors.New("unable to type assert volume")
		}
		if t.High, ok = response[11].(float64); !ok {
			return nil, errors.New("unable to type assert high")
		}
		if t.Low, ok = response[12].(float64); !ok {
			return nil, errors.New("unable to type assert low")
		}
		if t.FFRAmountAvailable, ok = response[15].(float64); !ok {
			return nil, errors.New("unable to type assert FFRAmountAvailable")
		}
		return &t, nil
	}

	var ok bool
	if t.Bid, ok = response[0].(float64); !ok {
		return nil, errors.New("unable to type assert bid")
	}
	if t.BidSize, ok = response[1].(float64); !ok {
		return nil, errors.New("unable to type assert bidSize")
	}
	if t.Ask, ok = response[2].(float64); !ok {
		return nil, errors.New("unable to type assert ask")
	}
	if t.AskSize, ok = response[3].(float64); !ok {
		return nil, errors.New("unable to type assert askSize")
	}
	if t.DailyChange, ok = response[4].(float64); !ok {
		return nil, errors.New("unable to type assert dailyChange")
	}
	if t.DailyChangePerc, ok = response[5].(float64); !ok {
		return nil, errors.New("unable to type assert dailyChangePerc")
	}
	if t.Last, ok = response[6].(float64); !ok {
		return nil, errors.New("unable to type assert last")
	}
	if t.Volume, ok = response[7].(float64); !ok {
		return nil, errors.New("unable to type assert volume")
	}
	if t.High, ok = response[8].(float64); !ok {
		return nil, errors.New("unable to type assert high")
	}
	if t.Low, ok = response[9].(float64); !ok {
		return nil, errors.New("unable to type assert low")
	}
	return &t, nil
}

// GetTrades gets historic trades that occurred on the exchange
//
// currencyPair e.g. "tBTCUSD"
// timestampStart is a millisecond timestamp
// timestampEnd is a millisecond timestamp
// reOrderResp reorders the returned data.
func (b *Bitfinex) GetTrades(ctx context.Context, currencyPair string, limit, timestampStart, timestampEnd int64, reOrderResp bool) ([]Trade, error) {
	v := url.Values{}
	if limit > 0 {
		v.Set("limit", strconv.FormatInt(limit, 10))
	}

	if timestampStart > 0 {
		v.Set("start", strconv.FormatInt(timestampStart, 10))
	}

	if timestampEnd > 0 {
		v.Set("end", strconv.FormatInt(timestampEnd, 10))
	}
	sortVal := "0"
	if reOrderResp {
		sortVal = "1"
	}
	v.Set("sort", sortVal)

	path := bitfinexAPIVersion2 + bitfinexTrades + currencyPair + "/hist" + "?" + v.Encode()

	var resp [][]interface{}
	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &resp, tradeRateLimit)
	if err != nil {
		return nil, err
	}

	history := make([]Trade, len(resp))
	for i := range resp {
		amount, ok := resp[i][2].(float64)
		if !ok {
			return nil, errors.New("unable to type assert amount")
		}
		side := order.Buy.String()
		if amount < 0 {
			side = order.Sell.String()
			amount *= -1
		}

		tid, ok := resp[i][0].(float64)
		if !ok {
			return nil, errors.New("unable to type assert trade ID")
		}
		timestamp, ok := resp[i][1].(float64)
		if !ok {
			return nil, errors.New("unable to type assert timestamp")
		}

		if len(resp[i]) > 4 {
			var rate float64
			rate, ok = resp[i][3].(float64)
			if !ok {
				return nil, errors.New("unable to type assert rate")
			}
			var period float64
			period, ok = resp[i][4].(float64)
			if !ok {
				return nil, errors.New("unable to type assert period")
			}

			history[i] = Trade{
				TID:       int64(tid),
				Timestamp: int64(timestamp),
				Amount:    amount,
				Rate:      rate,
				Period:    int64(period),
				Type:      side,
			}
			continue
		}
		price, ok := resp[i][3].(float64)
		if !ok {
			return nil, errors.New("unable to type assert price")
		}

		history[i] = Trade{
			TID:       int64(tid),
			Timestamp: int64(timestamp),
			Amount:    amount,
			Price:     price,
			Type:      side,
		}
	}

	return history, nil
}

// GetOrderbook retrieves the orderbook bid and ask price points for a currency
// pair - By default the response will return 25 bid and 25 ask price points.
// symbol - Example "tBTCUSD"
// precision - P0,P1,P2,P3,R0
// Values can contain limit amounts for both the asks and bids - Example
// "len" = 100
func (b *Bitfinex) GetOrderbook(ctx context.Context, symbol, precision string, limit int64) (Orderbook, error) {
	var u = url.Values{}
	if limit > 0 {
		u.Set("len", strconv.FormatInt(limit, 10))
	}
	path := bitfinexAPIVersion2 + bitfinexOrderbook + symbol + "/" + precision + "?" + u.Encode()
	var response [][]interface{}
	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, orderbookFunction)
	if err != nil {
		return Orderbook{}, err
	}

	var o Orderbook
	if precision == "R0" {
		// Raw book changes the return
		for x := range response {
			var b Book
			if len(response[x]) > 3 {
				// Funding currency
				var ok bool
				if b.Amount, ok = response[x][3].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert amount")
				}
				if b.Rate, ok = response[x][2].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert rate")
				}
				if b.Period, ok = response[x][1].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert period")
				}
				orderID, ok := response[x][0].(float64)
				if !ok {
					return Orderbook{}, errors.New("unable to type assert orderID")
				}
				b.OrderID = int64(orderID)
				if b.Amount > 0 {
					o.Asks = append(o.Asks, b)
				} else {
					b.Amount *= -1
					o.Bids = append(o.Bids, b)
				}
			} else {
				// Trading currency
				var ok bool
				if b.Amount, ok = response[x][2].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert amount")
				}
				if b.Price, ok = response[x][1].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert price")
				}
				orderID, ok := response[x][0].(float64)
				if !ok {
					return Orderbook{}, errors.New("unable to type assert order ID")
				}
				b.OrderID = int64(orderID)
				if b.Amount > 0 {
					o.Bids = append(o.Bids, b)
				} else {
					b.Amount *= -1
					o.Asks = append(o.Asks, b)
				}
			}
		}
	} else {
		for x := range response {
			var b Book
			if len(response[x]) > 3 {
				// Funding currency
				var ok bool
				if b.Amount, ok = response[x][3].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert amount")
				}
				count, ok := response[x][2].(float64)
				if !ok {
					return Orderbook{}, errors.New("unable to type assert count")
				}
				b.Count = int64(count)
				if b.Period, ok = response[x][1].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert period")
				}
				if b.Rate, ok = response[x][0].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert rate")
				}
				if b.Amount > 0 {
					o.Asks = append(o.Asks, b)
				} else {
					b.Amount *= -1
					o.Bids = append(o.Bids, b)
				}
			} else {
				// Trading currency
				var ok bool
				if b.Amount, ok = response[x][2].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert amount")
				}
				count, ok := response[x][1].(float64)
				if !ok {
					return Orderbook{}, errors.New("unable to type assert count")
				}
				b.Count = int64(count)
				if b.Price, ok = response[x][0].(float64); !ok {
					return Orderbook{}, errors.New("unable to type assert price")
				}
				if b.Amount > 0 {
					o.Bids = append(o.Bids, b)
				} else {
					b.Amount *= -1
					o.Asks = append(o.Asks, b)
				}
			}
		}
	}

	return o, nil
}

// GetStats returns various statistics about the requested pair
func (b *Bitfinex) GetStats(ctx context.Context, symbol string) ([]Stat, error) {
	var response []Stat
	path := bitfinexAPIVersion + bitfinexStats + symbol
	return response, b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, statsV1)
}

// GetFundingBook the entire margin funding book for both bids and asks sides
// per currency string
// symbol - example "USD"
// WARNING: Orderbook now has this support, will be deprecated once a full
// conversion to full V2 API update is done.
func (b *Bitfinex) GetFundingBook(ctx context.Context, symbol string) (FundingBook, error) {
	response := FundingBook{}
	path := bitfinexAPIVersion + bitfinexLendbook + symbol

	if err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, fundingbook); err != nil {
		return response, err
	}

	return response, nil
}

// GetLends returns a list of the most recent funding data for the given
// currency: total amount provided and Flash Return Rate (in % by 365 days)
// over time
// Symbol - example "USD"
func (b *Bitfinex) GetLends(ctx context.Context, symbol string, values url.Values) ([]Lends, error) {
	var response []Lends
	path := common.EncodeURLValues(bitfinexAPIVersion+
		bitfinexLends+
		symbol,
		values)
	return response, b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, lends)
}

// GetCandles returns candle chart data
// timeFrame values: '1m', '5m', '15m', '30m', '1h', '3h', '6h', '12h', '1D', '1W', '14D', '1M'
// section values: last or hist
func (b *Bitfinex) GetCandles(ctx context.Context, symbol, timeFrame string, start, end int64, limit uint32, historic bool) ([]Candle, error) {
	var fundingPeriod string
	if symbol[0] == 'f' {
		fundingPeriod = ":p30"
	}

	var path = bitfinexAPIVersion2 +
		bitfinexCandles +
		":" +
		timeFrame +
		":" +
		symbol +
		fundingPeriod

	if historic {
		v := url.Values{}
		if start > 0 {
			v.Set("start", strconv.FormatInt(start, 10))
		}

		if end > 0 {
			v.Set("end", strconv.FormatInt(end, 10))
		}

		if limit > 0 {
			v.Set("limit", strconv.FormatInt(int64(limit), 10))
		}

		path += "/hist"
		if len(v) > 0 {
			path += "?" + v.Encode()
		}

		var response [][]interface{}
		err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, candle)
		if err != nil {
			return nil, err
		}

		candles := make([]Candle, len(response))
		for i := range response {
			var c Candle
			timestamp, ok := response[i][0].(float64)
			if !ok {
				return nil, errors.New("unable to type assert timestamp")
			}
			c.Timestamp = time.UnixMilli(int64(timestamp))
			if c.Open, ok = response[i][1].(float64); !ok {
				return nil, errors.New("unable to type assert open")
			}
			if c.Close, ok = response[i][2].(float64); !ok {
				return nil, errors.New("unable to type assert close")
			}
			if c.High, ok = response[i][3].(float64); !ok {
				return nil, errors.New("unable to type assert high")
			}
			if c.Low, ok = response[i][4].(float64); !ok {
				return nil, errors.New("unable to type assert low")
			}
			if c.Volume, ok = response[i][5].(float64); !ok {
				return nil, errors.New("unable to type assert volume")
			}
			candles[i] = c
		}

		return candles, nil
	}

	path += "/last"

	var response []interface{}
	err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &response, candle)
	if err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, errors.New("no data returned")
	}

	var c Candle
	timestamp, ok := response[0].(float64)
	if !ok {
		return nil, errors.New("unable to type assert timestamp")
	}
	c.Timestamp = time.UnixMilli(int64(timestamp))
	if c.Open, ok = response[1].(float64); !ok {
		return nil, errors.New("unable to type assert open")
	}
	if c.Close, ok = response[2].(float64); !ok {
		return nil, errors.New("unable to type assert close")
	}
	if c.High, ok = response[3].(float64); !ok {
		return nil, errors.New("unable to type assert high")
	}
	if c.Low, ok = response[4].(float64); !ok {
		return nil, errors.New("unable to type assert low")
	}
	if c.Volume, ok = response[5].(float64); !ok {
		return nil, errors.New("unable to type assert volume")
	}

	return []Candle{c}, nil
}

// GetConfigurations fetches currency and symbol site configuration data.
func (b *Bitfinex) GetConfigurations() error {
	return common.ErrNotYetImplemented
}

// GetStatus returns different types of platform information - currently
// supports derivatives pair status only.
func (b *Bitfinex) GetStatus() error {
	return common.ErrNotYetImplemented
}

// GetLiquidationFeed returns liquidations. By default it will retrieve the most
// recent liquidations, but time-specific data can be retrieved using
// timestamps.
func (b *Bitfinex) GetLiquidationFeed() error {
	return common.ErrNotYetImplemented
}

// GetLeaderboard returns leaderboard standings for unrealized profit (period
// delta), unrealized profit (inception), volume, and realized profit.
// Allowed key values: "plu_diff" for unrealized profit (period delta), "plu"
// for unrealized profit (inception); "vol" for volume; "plr" for realized
// profit
// Allowed time frames are 3h, 1w and 1M
// Allowed symbols are trading pairs (e.g. tBTCUSD, tETHUSD and tGLOBAL:USD)
func (b *Bitfinex) GetLeaderboard(ctx context.Context, key, timeframe, symbol string, sort, limit int, start, end string) ([]LeaderboardEntry, error) {
	validLeaderboardKey := func(input string) bool {
		switch input {
		case LeaderboardUnrealisedProfitPeriodDelta,
			LeaderboardUnrealisedProfitInception,
			LeaderboardVolume,
			LeaderbookRealisedProfit:
			return true
		default:
			return false
		}
	}

	if !validLeaderboardKey(key) {
		return nil, errors.New("invalid leaderboard key")
	}

	path := fmt.Sprintf("%s/%s:%s:%s/hist", bitfinexAPIVersion2+bitfinexLeaderboard,
		key,
		timeframe,
		symbol)
	vals := url.Values{}
	if sort != 0 {
		vals.Set("sort", strconv.Itoa(sort))
	}
	if limit != 0 {
		vals.Set("limit", strconv.Itoa(limit))
	}
	if start != "" {
		vals.Set("start", start)
	}
	if end != "" {
		vals.Set("end", end)
	}
	path = common.EncodeURLValues(path, vals)
	var resp []interface{}
	if err := b.SendHTTPRequest(ctx, exchange.RestSpot, path, &resp, leaderBoardReqRate); err != nil {
		return nil, err
	}

	parseTwitterHandle := func(i interface{}) string {
		r, ok := i.(string)
		if !ok {
			return ""
		}
		return r
	}

	result := make([]LeaderboardEntry, len(resp))
	for x := range resp {
		r, ok := resp[x].([]interface{})
		if !ok {
			return nil, errors.New("unable to type assert leaderboard")
		}
		if len(r) < 10 {
			return nil, errors.New("unexpected leaderboard data length")
		}
		tm, ok := r[0].(float64)
		if !ok {
			return nil, errors.New("unable to type assert time")
		}
		username, ok := r[2].(string)
		if !ok {
			return nil, errors.New("unable to type assert username")
		}
		ranking, ok := r[3].(float64)
		if !ok {
			return nil, errors.New("unable to type assert ranking")
		}
		value, ok := r[6].(float64)
		if !ok {
			return nil, errors.New("unable to type assert value")
		}
		result[x] = LeaderboardEntry{
			Timestamp:     time.UnixMilli(int64(tm)),
			Username:      username,
			Ranking:       int(ranking),
			Value:         value,
			TwitterHandle: parseTwitterHandle(r[9]),
		}
	}
	return result, nil
}

// GetMarketAveragePrice calculates the average execution price for Trading or
// rate for Margin funding
func (b *Bitfinex) GetMarketAveragePrice() error {
	return common.ErrNotYetImplemented
}

// GetForeignExchangeRate calculates the exchange rate between two currencies
func (b *Bitfinex) GetForeignExchangeRate() error {
	return common.ErrNotYetImplemented
}

// GetAccountFees returns information about your account trading fees
func (b *Bitfinex) GetAccountFees(ctx context.Context) ([]AccountInfo, error) {
	var responses []AccountInfo
	return responses, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexAccountInfo,
		nil,
		&responses,
		getAccountFees)
}

// GetWithdrawalFees - Gets all fee rates for withdrawals
func (b *Bitfinex) GetWithdrawalFees(ctx context.Context) (AccountFees, error) {
	response := AccountFees{}
	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexAccountFees,
		nil,
		&response,
		getWithdrawalFees)
}

// GetAccountSummary returns a 30-day summary of your trading volume and return
// on margin funding
func (b *Bitfinex) GetAccountSummary(ctx context.Context) (AccountSummary, error) {
	response := AccountSummary{}

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexAccountSummary,
		nil,
		&response,
		getAccountSummary)
}

// NewDeposit returns a new deposit address
// Method - Example methods accepted: “bitcoin”, “litecoin”, “ethereum”,
// “tethers", "ethereumc", "zcash", "monero", "iota", "bcash"
// WalletName - accepted: "exchange", "margin", "funding" (can also use the old labels
// which are "exchange", "trading" and "deposit" respectively). If none is set,
// "funding" will be used by default
// renew - Default is 0. If set to 1, will return a new unused deposit address
func (b *Bitfinex) NewDeposit(ctx context.Context, method, walletName string, renew uint8) (*Deposit, error) {
	if walletName == "" {
		walletName = "funding"
	} else if !common.StringDataCompare(AcceptedWalletNames, walletName) {
		return nil,
			fmt.Errorf("walletname: [%s] is not allowed, supported: %s",
				walletName,
				AcceptedWalletNames)
	}

	req := make(map[string]interface{}, 3)
	req["wallet"] = walletName
	req["method"] = strings.ToLower(method)
	req["op_renew"] = renew
	var result []interface{}

	err := b.SendAuthenticatedHTTPRequestV2(ctx,
		exchange.RestSpot,
		http.MethodPost,
		bitfinexDepositAddress,
		req,
		&result,
		newDepositAddress)
	if err != nil {
		return nil, err
	}

	if len(result) != 8 {
		return nil, errors.New("expected result to have a len of 8")
	}

	depositInfo, ok := result[4].([]interface{})
	if !ok || len(depositInfo) != 6 {
		return nil, errors.New("unable to get deposit data")
	}
	depositMethod, ok := depositInfo[1].(string)
	if !ok {
		return nil, errors.New("unable to type assert depositMethod to string")
	}
	coin, ok := depositInfo[2].(string)
	if !ok {
		return nil, errors.New("unable to type assert coin to string")
	}
	var address, poolAddress string
	if depositInfo[5] == nil {
		address, ok = depositInfo[4].(string)
		if !ok {
			return nil, errors.New("unable to type assert address to string")
		}
	} else {
		poolAddress, ok = depositInfo[4].(string)
		if !ok {
			return nil, errors.New("unable to type assert poolAddress to string")
		}
		address, ok = depositInfo[5].(string)
		if !ok {
			return nil, errors.New("unable to type assert address to string")
		}
	}

	return &Deposit{
		Method:       depositMethod,
		CurrencyCode: coin,
		Address:      address,
		PoolAddress:  poolAddress,
	}, nil
}

// GetKeyPermissions checks the permissions of the key being used to generate
// this request.
func (b *Bitfinex) GetKeyPermissions(ctx context.Context) (KeyPermissions, error) {
	response := KeyPermissions{}
	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexKeyPermissions,
		nil,
		&response,
		getAccountFees)
}

// GetMarginInfo shows your trading wallet information for margin trading
func (b *Bitfinex) GetMarginInfo(ctx context.Context) ([]MarginInfo, error) {
	var response []MarginInfo
	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexMarginInfo,
		nil,
		&response,
		getMarginInfo)
}

// GetAccountBalance returns full wallet balance information
func (b *Bitfinex) GetAccountBalance(ctx context.Context) ([]Balance, error) {
	var response []Balance
	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexBalances,
		nil,
		&response,
		getAccountBalance)
}

// WalletTransfer move available balances between your wallets
// Amount - Amount to move
// Currency -  example "BTC"
// WalletFrom - example "exchange"
// WalletTo -  example "deposit"
func (b *Bitfinex) WalletTransfer(ctx context.Context, amount float64, currency, walletFrom, walletTo string) (WalletTransfer, error) {
	var response []WalletTransfer
	req := make(map[string]interface{})
	req["amount"] = strconv.FormatFloat(amount, 'f', -1, 64)
	req["currency"] = currency
	req["walletfrom"] = walletFrom
	req["walletto"] = walletTo

	err := b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexTransfer,
		req,
		&response,
		walletTransfer)
	if err != nil {
		return WalletTransfer{}, err
	}

	if response[0].Status == "error" {
		return WalletTransfer{}, errors.New(response[0].Message)
	}
	return response[0], nil
}

// WithdrawCryptocurrency requests a withdrawal from one of your wallets.
// For FIAT, use WithdrawFIAT
func (b *Bitfinex) WithdrawCryptocurrency(ctx context.Context, wallet, address, paymentID, curr string, amount float64) (Withdrawal, error) {
	var response []Withdrawal
	req := make(map[string]interface{})
	req["withdraw_type"] = strings.ToLower(curr)
	req["walletselected"] = wallet
	req["amount"] = strconv.FormatFloat(amount, 'f', -1, 64)
	req["address"] = address
	if paymentID != "" {
		req["payment_id"] = paymentID
	}

	err := b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexWithdrawal,
		req,
		&response,
		withdrawV1)
	if err != nil {
		return Withdrawal{}, err
	}

	if response[0].Status == "error" {
		return Withdrawal{}, errors.New(response[0].Message)
	}

	return response[0], nil
}

// WithdrawFIAT Sends an authenticated request to withdraw FIAT currency
func (b *Bitfinex) WithdrawFIAT(ctx context.Context, withdrawalType, walletType string, withdrawRequest *withdraw.Request) (Withdrawal, error) {
	var response []Withdrawal
	req := make(map[string]interface{})

	req["withdraw_type"] = withdrawalType
	req["walletselected"] = walletType
	req["amount"] = strconv.FormatFloat(withdrawRequest.Amount, 'f', -1, 64)
	req["account_name"] = withdrawRequest.Fiat.Bank.AccountName
	req["account_number"] = withdrawRequest.Fiat.Bank.AccountNumber
	req["bank_name"] = withdrawRequest.Fiat.Bank.BankName
	req["bank_address"] = withdrawRequest.Fiat.Bank.BankAddress
	req["bank_city"] = withdrawRequest.Fiat.Bank.BankPostalCity
	req["bank_country"] = withdrawRequest.Fiat.Bank.BankCountry
	req["expressWire"] = withdrawRequest.Fiat.IsExpressWire
	req["swift"] = withdrawRequest.Fiat.Bank.SWIFTCode
	req["detail_payment"] = withdrawRequest.Description
	req["currency"] = withdrawRequest.Currency
	req["account_address"] = withdrawRequest.Fiat.Bank.BankAddress

	if withdrawRequest.Fiat.RequiresIntermediaryBank {
		req["intermediary_bank_name"] = withdrawRequest.Fiat.IntermediaryBankName
		req["intermediary_bank_address"] = withdrawRequest.Fiat.IntermediaryBankAddress
		req["intermediary_bank_city"] = withdrawRequest.Fiat.IntermediaryBankCity
		req["intermediary_bank_country"] = withdrawRequest.Fiat.IntermediaryBankCountry
		req["intermediary_bank_account"] = strconv.FormatFloat(withdrawRequest.Fiat.IntermediaryBankAccountNumber, 'f', -1, 64)
		req["intermediary_bank_swift"] = withdrawRequest.Fiat.IntermediarySwiftCode
	}

	err := b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexWithdrawal,
		req,
		&response,
		withdrawV1)
	if err != nil {
		return Withdrawal{}, err
	}

	if response[0].Status == "error" {
		return Withdrawal{}, errors.New(response[0].Message)
	}

	return response[0], nil
}

// NewOrder submits a new order and returns a order information
// Major Upgrade needed on this function to include all query params
func (b *Bitfinex) NewOrder(ctx context.Context, currencyPair, orderType string, amount, price float64, buy, hidden bool) (Order, error) {
	if !common.StringDataCompare(AcceptedOrderType, orderType) {
		return Order{}, fmt.Errorf("order type %s not accepted", orderType)
	}

	response := Order{}
	req := make(map[string]interface{})
	req["symbol"] = currencyPair
	req["amount"] = strconv.FormatFloat(amount, 'f', -1, 64)
	req["price"] = strconv.FormatFloat(price, 'f', -1, 64)
	req["type"] = orderType
	req["is_hidden"] = hidden
	req["side"] = order.Sell.Lower()
	if buy {
		req["side"] = order.Buy.Lower()
	}

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderNew,
		req,
		&response,
		orderV1)
}

// OrderUpdate will send an update signal for an existing order
// and attempt to modify it
func (b *Bitfinex) OrderUpdate(ctx context.Context, orderID, groupID, clientOrderID string, amount, price, leverage float64) (*Order, error) {
	req := make(map[string]interface{})
	if orderID != "" {
		req["id"] = orderID
	}
	if groupID != "" {
		req["gid"] = groupID
	}
	if clientOrderID != "" {
		req["cid"] = clientOrderID
	}
	req["price"] = strconv.FormatFloat(price, 'f', -1, 64)
	req["amount"] = strconv.FormatFloat(amount, 'f', -1, 64)
	if leverage > 1 {
		req["lev"] = strconv.FormatFloat(leverage, 'f', -1, 64)
	}
	response := Order{}
	return &response, b.SendAuthenticatedHTTPRequestV2(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderUpdate,
		req,
		&response,
		orderV1)
}

// NewOrderMulti allows several new orders at once
func (b *Bitfinex) NewOrderMulti(ctx context.Context, orders []PlaceOrder) (OrderMultiResponse, error) {
	response := OrderMultiResponse{}
	req := make(map[string]interface{})
	req["orders"] = orders

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderNewMulti,
		req,
		&response,
		orderMulti)
}

// CancelExistingOrder cancels a single order by OrderID
func (b *Bitfinex) CancelExistingOrder(ctx context.Context, orderID int64) (Order, error) {
	response := Order{}
	req := make(map[string]interface{})
	req["order_id"] = orderID

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderCancel,
		req,
		&response,
		orderMulti)
}

// CancelMultipleOrders cancels multiple orders
func (b *Bitfinex) CancelMultipleOrders(ctx context.Context, orderIDs []int64) (string, error) {
	response := GenericResponse{}
	req := make(map[string]interface{})
	req["order_ids"] = orderIDs
	return response.Result, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderCancelMulti,
		req,
		nil,
		orderMulti)
}

// CancelMultipleOrdersV2 cancels multiple orders
func (b *Bitfinex) CancelMultipleOrdersV2(ctx context.Context, orderID, clientOrderID, groupOrderID int64, clientOrderIDDate time.Time, allOrders bool) ([]CancelMultiOrderResponse, error) {
	var response []interface{}
	req := make(map[string]interface{})
	if orderID > 0 {
		req["id"] = orderID
	}
	if clientOrderID > 0 {
		req["cid"] = clientOrderID
	}
	if !clientOrderIDDate.IsZero() {
		req["cid_date"] = clientOrderIDDate.Format("2006-01-02")
	}
	if groupOrderID > 0 {
		req["gid"] = groupOrderID
	}
	if allOrders {
		req["all"] = 1
	}

	err := b.SendAuthenticatedHTTPRequestV2(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderCancelMulti,
		req,
		&response,
		orderMulti)
	if err != nil {
		return nil, err
	}
	var cancelledOrders []CancelMultiOrderResponse
	for x := range response {
		cancelledOrdersSlice, ok := response[x].([]interface{})
		if !ok {
			continue
		}
		for y := range cancelledOrdersSlice {
			cancelledOrderFields, ok := cancelledOrdersSlice[y].([]interface{})
			if !ok {
				continue
			}
			var cancelledOrder CancelMultiOrderResponse
			for z := range cancelledOrderFields {
				switch z {
				case 0:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "ID")
					}
					cancelledOrder.OrderID = strconv.FormatFloat(f, 'f', -1, 64)
				case 1:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "GID")
					}
					cancelledOrder.GroupOrderID = strconv.FormatFloat(f, 'f', -1, 64)
				case 2:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "CID")
					}
					cancelledOrder.ClientOrderID = strconv.FormatFloat(f, 'f', -1, 64)
				case 3:
					f, ok := cancelledOrderFields[z].(string)
					if !ok {
						return nil, common.GetTypeAssertError("string", cancelledOrderFields[z], "SYMBOL")
					}
					cancelledOrder.Symbol = f
				case 4:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "MTS_CREATE")
					}
					cancelledOrder.CreatedTime = time.UnixMilli(int64(f))
				case 5:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "MTS_UPDATE")
					}
					cancelledOrder.UpdatedTime = time.UnixMilli(int64(f))
				case 6:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "AMOUNT")
					}
					cancelledOrder.Amount = f
				case 7:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "AMOUNT_ORIG")
					}
					cancelledOrder.OriginalAmount = f
				case 8:
					f, ok := cancelledOrderFields[z].(string)
					if !ok {
						return nil, common.GetTypeAssertError("string", cancelledOrderFields[z], "TYPE")
					}
					cancelledOrder.OrderType = f
				case 9:
					f, ok := cancelledOrderFields[z].(string)
					if !ok {
						return nil, common.GetTypeAssertError("string", cancelledOrderFields[z], "TYPE_PREV")
					}
					cancelledOrder.OriginalOrderType = f
				case 12:
					f, ok := cancelledOrderFields[z].(string)
					if !ok {
						return nil, common.GetTypeAssertError("string", cancelledOrderFields[z], "FLAGS")
					}
					cancelledOrder.OrderFlags = f
				case 13:
					f, ok := cancelledOrderFields[z].(string)
					if !ok {
						return nil, common.GetTypeAssertError("string", cancelledOrderFields[z], "ORDER_STATUS")
					}
					cancelledOrder.OrderStatus = f
				case 16:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "PRICE")
					}
					cancelledOrder.Price = f
				case 17:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "PRICE_AVG")
					}
					cancelledOrder.AveragePrice = f
				case 18:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "PRICE_TRAILING")
					}
					cancelledOrder.TrailingPrice = f
				case 19:
					f, ok := cancelledOrderFields[z].(float64)
					if !ok {
						return nil, common.GetTypeAssertError("float64", cancelledOrderFields[z], "PRICE_AUX_LIMIT")
					}
					cancelledOrder.AuxLimitPrice = f
				}
			}
			cancelledOrders[y] = cancelledOrder
		}
	}
	return cancelledOrders, nil
}

// CancelAllExistingOrders cancels all active and open orders
func (b *Bitfinex) CancelAllExistingOrders(ctx context.Context) (string, error) {
	response := GenericResponse{}

	return response.Result, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderCancelAll,
		nil,
		nil,
		orderMulti)
}

// ReplaceOrder replaces an older order with a new order
func (b *Bitfinex) ReplaceOrder(ctx context.Context, orderID int64, symbol string, amount, price float64, buy bool, orderType string, hidden bool) (Order, error) {
	response := Order{}
	req := make(map[string]interface{})
	req["order_id"] = orderID
	req["symbol"] = symbol
	req["amount"] = strconv.FormatFloat(amount, 'f', -1, 64)
	req["price"] = strconv.FormatFloat(price, 'f', -1, 64)
	req["exchange"] = "bitfinex"
	req["type"] = orderType
	req["is_hidden"] = hidden

	if buy {
		req["side"] = order.Buy.Lower()
	} else {
		req["side"] = order.Sell.Lower()
	}

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderCancelReplace,
		req,
		&response,
		orderMulti)
}

// GetOrderStatus returns order status information
func (b *Bitfinex) GetOrderStatus(ctx context.Context, orderID int64) (Order, error) {
	orderStatus := Order{}
	req := make(map[string]interface{})
	req["order_id"] = orderID

	return orderStatus, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderStatus,
		req,
		&orderStatus,
		orderMulti)
}

// GetInactiveOrders returns order status information
func (b *Bitfinex) GetInactiveOrders(ctx context.Context, symbol string, ids ...int64) ([]Order, error) {
	var response []Order
	req := make(map[string]interface{})
	req["limit"] = 2500
	if len(ids) > 0 {
		req["ids"] = ids
	}
	return response, b.SendAuthenticatedHTTPRequestV2(
		ctx,
		exchange.RestSpot,
		http.MethodPost,
		bitfinexV2Auth+"r/"+bitfinexOrders+"/"+symbol+"/"+bitfinexInactiveOrders,
		req,
		&response,
		orderMulti)
}

// GetOpenOrders returns all active orders and statuses
func (b *Bitfinex) GetOpenOrders(ctx context.Context, ids ...int64) ([]Order, error) {
	var response []Order
	req := make(map[string]interface{})
	if len(ids) > 0 {
		req["ids"] = ids
	}
	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrders,
		req,
		&response,
		orderMulti)
}

// GetActivePositions returns an array of active positions
func (b *Bitfinex) GetActivePositions(ctx context.Context) ([]Position, error) {
	var response []Position

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexPositions,
		nil,
		&response,
		orderMulti)
}

// ClaimPosition allows positions to be claimed
func (b *Bitfinex) ClaimPosition(ctx context.Context, positionID int) (Position, error) {
	response := Position{}
	req := make(map[string]interface{})
	req["position_id"] = positionID

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexClaimPosition,
		nil,
		nil,
		orderMulti)
}

// GetBalanceHistory returns balance history for the account
func (b *Bitfinex) GetBalanceHistory(ctx context.Context, symbol string, timeSince, timeUntil time.Time, limit int, wallet string) ([]BalanceHistory, error) {
	var response []BalanceHistory
	req := make(map[string]interface{})
	req["currency"] = symbol

	if !timeSince.IsZero() {
		req["since"] = timeSince
	}
	if !timeUntil.IsZero() {
		req["until"] = timeUntil
	}
	if limit > 0 {
		req["limit"] = limit
	}
	if len(wallet) > 0 {
		req["wallet"] = wallet
	}

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexHistory,
		req,
		&response,
		orderMulti)
}

// GetMovementHistory returns an array of past deposits and withdrawals
func (b *Bitfinex) GetMovementHistory(ctx context.Context, symbol, method string, timeSince, timeUntil time.Time, limit int) ([]MovementHistory, error) {
	var response [][]interface{}
	req := make(map[string]interface{})
	req["currency"] = symbol

	if len(method) > 0 {
		req["method"] = method
	}
	if !timeSince.IsZero() {
		req["since"] = timeSince
	}
	if !timeUntil.IsZero() {
		req["until"] = timeUntil
	}
	if limit > 0 {
		req["limit"] = limit
	}

	err := b.SendAuthenticatedHTTPRequestV2(ctx, exchange.RestSpot, http.MethodPost,
		"auth/r/"+bitfinexHistoryMovements+"/"+symbol+"/"+bitfinexHistoryShort,
		req,
		&response,
		orderMulti)
	if err != nil {
		return nil, err
	}
	var resp []MovementHistory //nolint:prealloc // its an array in an array
	var ok bool
	for i := range response {
		var move MovementHistory
		for j := range response[i] {
			if response[i][j] == nil {
				continue
			}
			switch j {
			case 0:
				var id float64
				id, ok = response[i][j].(float64)
				if !ok {
					return nil, common.GetTypeAssertError("float64", response[i][j], "ID")
				}
				move.ID = int64(id)
			case 1:
				move.Currency, ok = response[i][j].(string)
				if !ok {
					return nil, common.GetTypeAssertError("string", response[i][j], "CURRENCY")
				}
			case 5:
				move.TimestampCreated, ok = response[i][j].(float64)
				if !ok {
					return nil, common.GetTypeAssertError("float64", response[i][j], "MTS_STARTED")
				}
			case 6:
				move.Timestamp, ok = response[i][j].(float64)
				if !ok {
					return nil, common.GetTypeAssertError("float64", response[i][j], "MTS_UPDATED")
				}
			case 9:
				move.Status, ok = response[i][j].(string)
				if !ok {
					return nil, common.GetTypeAssertError("string", response[i][j], "STATUS")
				}
			case 12:
				move.Amount, ok = response[i][j].(float64)
				if !ok {
					return nil, common.GetTypeAssertError("float64", response[i][j], "AMOUNT")
				}
			case 13:
				move.Fee, ok = response[i][j].(float64)
				if !ok {
					return nil, common.GetTypeAssertError("float64", response[i][j], "FEE")
				}
			case 16:
				move.Address, ok = response[i][j].(string)
				if !ok {
					return nil, common.GetTypeAssertError("string", response[i][j], "DESTINATION_ADDRESS")
				}
			case 20:
				move.TxID, ok = response[i][j].(string)
				if !ok {
					return nil, common.GetTypeAssertError("string", response[i][j], "TRANSACTION_ID")
				}
			case 21:
				move.Description, ok = response[i][j].(string)
				if !ok {
					return nil, common.GetTypeAssertError("string", response[i][j], "WITHDRAW_TRANSACTION_NOTE")
				}
			}
		}
		resp = append(resp, move)
	}
	return resp, nil
}

// GetTradeHistory returns past executed trades
func (b *Bitfinex) GetTradeHistory(ctx context.Context, currencyPair string, timestamp, until time.Time, limit, reverse int) ([]TradeHistory, error) {
	var response []TradeHistory
	req := make(map[string]interface{})
	req["currency"] = currencyPair
	req["timestamp"] = timestamp

	if !until.IsZero() {
		req["until"] = until
	}
	if limit > 0 {
		req["limit"] = limit
	}
	if reverse > 0 {
		req["reverse"] = reverse
	}

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexTradeHistory,
		req,
		&response,
		orderMulti)
}

// NewOffer submits a new offer
func (b *Bitfinex) NewOffer(ctx context.Context, symbol string, amount, rate float64, period int64, direction string) (Offer, error) {
	response := Offer{}
	req := make(map[string]interface{})
	req["currency"] = symbol
	req["amount"] = amount
	req["rate"] = rate
	req["period"] = period
	req["direction"] = direction

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOfferNew,
		req,
		&response,
		orderMulti)
}

// CancelOffer cancels offer by offerID
func (b *Bitfinex) CancelOffer(ctx context.Context, offerID int64) (Offer, error) {
	response := Offer{}
	req := make(map[string]interface{})
	req["offer_id"] = offerID

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOfferCancel,
		req,
		&response,
		orderMulti)
}

// GetOfferStatus checks offer status whether it has been cancelled, execute or
// is still active
func (b *Bitfinex) GetOfferStatus(ctx context.Context, offerID int64) (Offer, error) {
	response := Offer{}
	req := make(map[string]interface{})
	req["offer_id"] = offerID

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOrderStatus,
		req,
		&response,
		orderMulti)
}

// GetActiveCredits returns all available credits
func (b *Bitfinex) GetActiveCredits(ctx context.Context) ([]Offer, error) {
	var response []Offer

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexActiveCredits,
		nil,
		&response,
		orderMulti)
}

// GetActiveOffers returns all current active offers
func (b *Bitfinex) GetActiveOffers(ctx context.Context) ([]Offer, error) {
	var response []Offer

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexOffers,
		nil,
		&response,
		orderMulti)
}

// GetActiveMarginFunding returns an array of active margin funds
func (b *Bitfinex) GetActiveMarginFunding(ctx context.Context) ([]MarginFunds, error) {
	var response []MarginFunds

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexMarginActiveFunds,
		nil,
		&response,
		orderMulti)
}

// GetUnusedMarginFunds returns an array of funding borrowed but not currently
// used
func (b *Bitfinex) GetUnusedMarginFunds(ctx context.Context) ([]MarginFunds, error) {
	var response []MarginFunds

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexMarginUnusedFunds,
		nil,
		&response,
		orderMulti)
}

// GetMarginTotalTakenFunds returns an array of active funding used in a
// position
func (b *Bitfinex) GetMarginTotalTakenFunds(ctx context.Context) ([]MarginTotalTakenFunds, error) {
	var response []MarginTotalTakenFunds

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexMarginTotalFunds,
		nil,
		&response,
		orderMulti)
}

// CloseMarginFunding closes an unused or used taken fund
func (b *Bitfinex) CloseMarginFunding(ctx context.Context, swapID int64) (Offer, error) {
	response := Offer{}
	req := make(map[string]interface{})
	req["swap_id"] = swapID

	return response, b.SendAuthenticatedHTTPRequest(ctx, exchange.RestSpot, http.MethodPost,
		bitfinexMarginClose,
		req,
		&response,
		closeFunding)
}

// SendHTTPRequest sends an unauthenticated request
func (b *Bitfinex) SendHTTPRequest(ctx context.Context, ep exchange.URL, path string, result interface{}, e request.EndpointLimit) error {
	endpoint, err := b.API.Endpoints.GetURL(ep)
	if err != nil {
		return err
	}
	item := &request.Item{
		Method:        http.MethodGet,
		Path:          endpoint + path,
		Result:        result,
		Verbose:       b.Verbose,
		HTTPDebugging: b.HTTPDebugging,
		HTTPRecording: b.HTTPRecording}

	return b.SendPayload(ctx, e, func() (*request.Item, error) {
		return item, nil
	}, request.UnauthenticatedRequest)
}

// SendAuthenticatedHTTPRequest sends an authenticated http request and json
// unmarshals result to a supplied variable
func (b *Bitfinex) SendAuthenticatedHTTPRequest(ctx context.Context, ep exchange.URL, method, path string, params map[string]interface{}, result interface{}, endpoint request.EndpointLimit) error {
	creds, err := b.GetCredentials(ctx)
	if err != nil {
		return err
	}

	ePoint, err := b.API.Endpoints.GetURL(ep)
	if err != nil {
		return err
	}

	fullPath := ePoint + bitfinexAPIVersion + path
	return b.SendPayload(ctx, endpoint, func() (*request.Item, error) {
		n := b.Requester.GetNonce(true)
		req := make(map[string]interface{})
		req["request"] = bitfinexAPIVersion + path
		req["nonce"] = n.String()

		for key, value := range params {
			req[key] = value
		}

		PayloadJSON, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}

		PayloadBase64 := crypto.Base64Encode(PayloadJSON)
		hmac, err := crypto.GetHMAC(crypto.HashSHA512_384,
			[]byte(PayloadBase64),
			[]byte(creds.Secret))
		if err != nil {
			return nil, err
		}
		headers := make(map[string]string)
		headers["X-BFX-APIKEY"] = creds.Key
		headers["X-BFX-PAYLOAD"] = PayloadBase64
		headers["X-BFX-SIGNATURE"] = crypto.HexEncodeToString(hmac)

		return &request.Item{
			Method:        method,
			Path:          fullPath,
			Headers:       headers,
			Result:        result,
			NonceEnabled:  true,
			Verbose:       b.Verbose,
			HTTPDebugging: b.HTTPDebugging,
			HTTPRecording: b.HTTPRecording}, nil
	}, request.AuthenticatedRequest)
}

// SendAuthenticatedHTTPRequestV2 sends an authenticated http request and json
// unmarshals result to a supplied variable
func (b *Bitfinex) SendAuthenticatedHTTPRequestV2(ctx context.Context, ep exchange.URL, method, path string, params map[string]interface{}, result interface{}, endpoint request.EndpointLimit) error {
	creds, err := b.GetCredentials(ctx)
	if err != nil {
		return err
	}
	ePoint, err := b.API.Endpoints.GetURL(ep)
	if err != nil {
		return err
	}

	return b.SendPayload(ctx, endpoint, func() (*request.Item, error) {
		var body io.Reader
		var payload []byte
		if len(params) != 0 {
			payload, err = json.Marshal(params)
			if err != nil {
				return nil, err
			}
			body = bytes.NewBuffer(payload)
		}

		n := strconv.FormatInt(time.Now().Unix()*1e9, 10)
		headers := make(map[string]string)
		headers["Content-Type"] = "application/json"
		headers["Accept"] = "application/json"
		headers["bfx-apikey"] = creds.Key
		headers["bfx-nonce"] = n
		sig := "/api" + bitfinexAPIVersion2 + path + n + string(payload)
		hmac, err := crypto.GetHMAC(
			crypto.HashSHA512_384,
			[]byte(sig),
			[]byte(creds.Secret),
		)
		if err != nil {
			return nil, err
		}
		headers["bfx-signature"] = crypto.HexEncodeToString(hmac)

		return &request.Item{
			Method:        method,
			Path:          ePoint + bitfinexAPIVersion2 + path,
			Headers:       headers,
			Body:          body,
			Result:        result,
			NonceEnabled:  true,
			Verbose:       b.Verbose,
			HTTPDebugging: b.HTTPDebugging,
			HTTPRecording: b.HTTPRecording,
		}, nil
	}, request.AuthenticatedRequest)
}

// GetFee returns an estimate of fee based on type of transaction
func (b *Bitfinex) GetFee(ctx context.Context, feeBuilder *exchange.FeeBuilder) (float64, error) {
	var fee float64

	switch feeBuilder.FeeType {
	case exchange.CryptocurrencyTradeFee:
		accountInfos, err := b.GetAccountFees(ctx)
		if err != nil {
			return 0, err
		}
		fee, err = b.CalculateTradingFee(accountInfos,
			feeBuilder.PurchasePrice,
			feeBuilder.Amount,
			feeBuilder.Pair.Base,
			feeBuilder.IsMaker)
		if err != nil {
			return 0, err
		}
	case exchange.CryptocurrencyDepositFee:
		//TODO: fee is charged when < $1000USD is transferred, need to infer value in some way
		fee = 0
	case exchange.CryptocurrencyWithdrawalFee:
		acc, err := b.GetWithdrawalFees(ctx)
		if err != nil {
			return 0, err
		}
		fee, err = b.GetCryptocurrencyWithdrawalFee(feeBuilder.Pair.Base, acc)
		if err != nil {
			return 0, err
		}
	case exchange.InternationalBankDepositFee:
		fee = getInternationalBankDepositFee(feeBuilder.Amount)
	case exchange.InternationalBankWithdrawalFee:
		fee = getInternationalBankWithdrawalFee(feeBuilder.Amount)
	case exchange.OfflineTradeFee:
		fee = getOfflineTradeFee(feeBuilder.PurchasePrice, feeBuilder.Amount)
	}
	if fee < 0 {
		fee = 0
	}
	return fee, nil
}

// getOfflineTradeFee calculates the worst case-scenario trading fee
// does not require an API request, requires manual updating
func getOfflineTradeFee(price, amount float64) float64 {
	return 0.001 * price * amount
}

// GetCryptocurrencyWithdrawalFee returns an estimate of fee based on type of transaction
func (b *Bitfinex) GetCryptocurrencyWithdrawalFee(c currency.Code, accountFees AccountFees) (fee float64, err error) {
	switch result := accountFees.Withdraw[c.String()].(type) {
	case string:
		fee, err = strconv.ParseFloat(result, 64)
		if err != nil {
			return 0, err
		}
	case float64:
		fee = result
	}

	return fee, nil
}

func getInternationalBankDepositFee(amount float64) float64 {
	return 0.001 * amount
}

func getInternationalBankWithdrawalFee(amount float64) float64 {
	return 0.001 * amount
}

// CalculateTradingFee returns an estimate of fee based on type of whether is maker or taker fee
func (b *Bitfinex) CalculateTradingFee(i []AccountInfo, purchasePrice, amount float64, c currency.Code, isMaker bool) (fee float64, err error) {
	for x := range i {
		for y := range i[x].Fees {
			if c.String() == i[x].Fees[y].Pairs {
				if isMaker {
					fee = i[x].Fees[y].MakerFees
				} else {
					fee = i[x].Fees[y].TakerFees
				}
				break
			}
		}
		if fee > 0 {
			break
		}
	}
	return (fee / 100) * purchasePrice * amount, err
}

// PopulateAcceptableMethods retrieves all accepted currency strings and
// populates a map to check
func (b *Bitfinex) PopulateAcceptableMethods(ctx context.Context) error {
	if acceptableMethods.loaded() {
		return nil
	}

	var response [][][]interface{}
	err := b.SendHTTPRequest(ctx,
		exchange.RestSpot,
		bitfinexAPIVersion2+bitfinexDepositMethod,
		&response,
		configs)
	if err != nil {
		return err
	}

	if len(response) == 0 {
		return errors.New("response contains no data cannot populate acceptable method map")
	}

	data := response[0]
	storeData := make(map[string][]string)
	for x := range data {
		if len(data[x]) == 0 {
			return fmt.Errorf("data should not be empty")
		}
		name, ok := data[x][0].(string)
		if !ok {
			return fmt.Errorf("unable to type assert name")
		}

		var availOptions []string
		options, ok := data[x][1].([]interface{})
		if !ok {
			return fmt.Errorf("unable to type assert options")
		}
		for x := range options {
			o, ok := options[x].(string)
			if !ok {
				return fmt.Errorf("unable to type assert option to string")
			}
			availOptions = append(availOptions, o)
		}
		storeData[name] = availOptions
	}
	acceptableMethods.load(storeData)
	return nil
}
