package conf

import (
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/agiledragon/gomonkey"
	"github.com/janeczku/go-ipset/ipset"
	"github.com/stretchr/testify/assert"
	"github.com/wolf-joe/ts-dns/cache"
	"github.com/wolf-joe/ts-dns/hosts"
	"github.com/wolf-joe/ts-dns/matcher"
	"github.com/wolf-joe/ts-dns/mock"
	"testing"
)

func TestGroup(t *testing.T) {
	mocker := mock.NewMocker()
	defer mocker.Reset()

	group := Group{}
	// 测试GenIPSet
	mocker.FuncSeq(ipset.New, []gomonkey.Params{
		{nil, fmt.Errorf("err")}, {&ipset.IPSet{}, nil},
	})
	s, err := group.GenIPSet() // ipset名称为空，直接返回nil
	assert.Nil(t, s)
	assert.Nil(t, err)
	group.IPSet = "test"
	s, err = group.GenIPSet() //ipset.New返回异常结果
	assert.Nil(t, s)
	assert.NotNil(t, err)
	s, err = group.GenIPSet() // ipset.New返回正常结果
	assert.NotNil(t, s)
	assert.Nil(t, err)

	// 测试GenCallers
	callers := group.GenCallers()
	assert.Empty(t, callers)
	group.Socks5 = "1.1.1.1"
	group.DNS = []string{"1.1.1.1", "8.8.8.8:53/tcp"}              // 两个都有效
	group.DoT = []string{"1.1.1.1", "1.1.1.1@name"}                // 后一个有效
	group.DoH = []string{"not exists", "https://domain/dns-query"} // 后一个有效
	callers = group.GenCallers()
	assert.Equal(t, len(callers), 4)
}

func TestConf(t *testing.T) {
	mocker := mock.NewMocker()
	defer mocker.Reset()

	conf := &Conf{}
	// 测试SetDefault
	conf.SetDefault()
	assert.NotEmpty(t, conf.Listen)
	assert.NotEmpty(t, conf.GFWList)
	assert.NotEmpty(t, conf.CNIP)
	// 测试GenCache
	conf.Cache = &Cache{}
	c := conf.GenCache()
	assert.NotNil(t, c)
	// 测试GenHostsReader
	conf.Hosts = map[string]string{"host": "1.1.1.1", "ne": "ne"}
	conf.HostsFiles = []string{"aaa", "bbb"} // 后一个NewReaderByFile正常
	mocker.FuncSeq(hosts.NewReaderByFile, []gomonkey.Params{
		{nil, fmt.Errorf("err")}, {&hosts.FileReader{}, nil},
	})
	readers := conf.GenHostsReader()
	assert.Equal(t, len(readers), 2)
	assert.NotNil(t, readers[0].IP("host", false))
}

func TestNewHandler(t *testing.T) {
	mocker := mock.NewMocker()
	defer mocker.Reset()

	mocker.FuncSeq(toml.DecodeFile, []gomonkey.Params{
		{nil, fmt.Errorf("err")}, {nil, nil}, {nil, nil}, {nil, nil},
	})
	handler, err := NewHandler("") // DecodeFile失败
	assert.Nil(t, handler)
	assert.NotNil(t, err)
	mocker.FuncSeq(matcher.NewABPByFile, []gomonkey.Params{
		{nil, fmt.Errorf("err")}, {nil, nil}, {nil, nil},
	})
	handler, err = NewHandler("") // NewABPByFile失败
	assert.Nil(t, handler)
	assert.NotNil(t, err)
	mocker.FuncSeq(cache.NewRamSetByFile, []gomonkey.Params{
		{nil, fmt.Errorf("err")}, {nil, nil},
	})
	handler, err = NewHandler("") // NewRamSetByFile失败
	assert.Nil(t, handler)
	assert.NotNil(t, err)
	mocker.MethodSeq(&Conf{}, "GenCache", []gomonkey.Params{{nil}})
	handler, err = NewHandler("") // NewRamSetByFile失败
	assert.Nil(t, handler)
	assert.NotNil(t, err)
}
