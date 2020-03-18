package inbound

import (
	log "github.com/Sirupsen/logrus"
	"github.com/janeczku/go-ipset/ipset"
	"github.com/miekg/dns"
	"github.com/wolf-joe/ts-dns/cache"
	"github.com/wolf-joe/ts-dns/hosts"
	"github.com/wolf-joe/ts-dns/matcher"
	"github.com/wolf-joe/ts-dns/outbound"
	"sync"
)

// Group 各域名组相关配置
type Group struct {
	Callers    []outbound.Caller
	Matcher    *matcher.ABPlus
	IPSet      *ipset.IPSet
	Concurrent bool
}

// CallDNS 依次向组内的dns服务器转发请求，获得非nil响应则返回
// 如指定ipRange、且dns响应中出现非ipRange范围内的ipv4地址，则视该响应无效
func (group *Group) CallDNS(request *dns.Msg, ipRange *cache.RamSet) *dns.Msg {
	if len(group.Callers) == 0 || request == nil {
		return nil
	}
	// 并发用的channel
	ch := make(chan *dns.Msg, len(group.Callers))
	// 包裹Caller.Call，方便实现并发
	call := func(caller outbound.Caller, request *dns.Msg) *dns.Msg {
		r, err := caller.Call(request)
		if err != nil {
			log.Errorf("query dns error: %v", err)
		} else if r != nil && ipRange != nil && !allInRange(r, ipRange) {
			r = nil
		}
		ch <- r
		return r
	}
	// 遍历DNS服务器
	for _, caller := range group.Callers {
		if group.Concurrent {
			go call(caller, request)
		} else if r := call(caller, request); r != nil {
			return r
		}
	}
	// 并发情况下依次提取channel中的返回值
	if group.Concurrent {
		for r := range ch {
			if r != nil {
				return r
			}
		}
	}
	return nil
}

// AddIPSet 将dns响应中所有的ipv4地址加入group指定的ipset
func (group *Group) AddIPSet(r *dns.Msg) {
	if group.IPSet == nil || r == nil {
		return
	}
	for _, ip := range extractA(r) {
		if err := group.IPSet.Add(ip, group.IPSet.Timeout); err != nil {
			log.Errorf("add ipset error: %v", err)
		}
	}
	return
}

// Handler 存储主要配置的dns请求处理器，程序核心
type Handler struct {
	Mux          *sync.RWMutex
	Listen       string
	Cache        *cache.DNSCache
	GFWMatcher   *matcher.ABPlus
	CNIP         *cache.RamSet
	HostsReaders []hosts.Reader
	GroupMap     map[string]*Group
}

// HitHosts 如dns请求匹配hosts，则生成对应dns记录并返回。否则返回nil
func (handler *Handler) HitHosts(request *dns.Msg) *dns.Msg {
	question := request.Question[0]
	if question.Qtype == dns.TypeA || question.Qtype == dns.TypeAAAA {
		ipv6 := question.Qtype == dns.TypeAAAA
		for _, reader := range handler.HostsReaders {
			record, hostname := "", question.Name
			if record = reader.Record(hostname, ipv6); record == "" {
				// 去掉末尾的根域名再找一次
				record = reader.Record(hostname[:len(hostname)-1], ipv6)
			}
			if record != "" {
				if ret, err := dns.NewRR(record); err != nil {
					log.Errorf("make DNS.RR error: %v", err)
				} else {
					r := new(dns.Msg)
					r.Answer = append(r.Answer, ret)
					return r
				}
			}
		}
	}
	return nil
}

// ServeDNS 处理dns请求，程序核心函数
func (handler *Handler) ServeDNS(resp dns.ResponseWriter, request *dns.Msg) {
	handler.Mux.RLock() // 申请读锁，持续整个请求
	var r *dns.Msg
	var group *Group
	defer func() {
		if r != nil {
			r.SetReply(request) // 写入响应
			_ = resp.WriteMsg(r)
		}
		if group != nil {
			group.AddIPSet(r) // 写入IPSet
		}
		handler.Mux.RUnlock() // 读锁解除
		_ = resp.Close()      // 结束连接
	}()

	question := request.Question[0]
	fields := log.Fields{"domain": question.Name, "src": resp.RemoteAddr()}
	// 检测是否命中hosts
	if r = handler.HitHosts(request); r != nil {
		log.WithFields(fields).Infof("hit hosts")
		return
	}
	// 检测是否命中dns缓存
	if r = handler.Cache.Get(request); r != nil {
		log.WithFields(fields).Infof("hit cache")
		return
	}

	// 判断域名是否匹配指定规则
	var name string
	for name, group = range handler.GroupMap {
		if match, ok := group.Matcher.Match(question.Name); ok && match {
			fields["group"] = name
			log.WithFields(fields).Infof("match by rules")
			r = group.CallDNS(request, nil)
			return
		}
	}
	// 先用clean组dns解析
	fields["group"], group = "clean", handler.GroupMap["clean"]
	r = group.CallDNS(request, handler.CNIP)
	if allInRange(r, handler.CNIP) {
		// 未出现非cn ip，流程结束
		log.WithFields(fields).Infof("cn/empty ipv4")
	} else if blocked, ok := handler.GFWMatcher.Match(question.Name); !ok || !blocked {
		// 出现非cn ip但域名不匹配gfwlist，流程结束
		log.WithFields(fields).Infof("not match gfwlist")
	} else {
		// 出现非cn ip且域名匹配gfwlist，用dirty组dns再次解析
		fields["group"], group = "dirty", handler.GroupMap["dirty"]
		log.WithFields(fields).Infof("match gfwlist")
		r = group.CallDNS(request, nil)
	}
	// 设置dns缓存
	handler.Cache.Set(request, r)
}

// Refresh 刷新配置，复制newHandler中除Mux、Listen之外的值
func (handler *Handler) Refresh(newHandler *Handler) {
	handler.Mux.Lock()
	defer handler.Mux.Unlock()

	if newHandler.Cache != nil {
		handler.Cache = newHandler.Cache
	}
	if newHandler.GFWMatcher != nil {
		handler.GFWMatcher = newHandler.GFWMatcher
	}
	if newHandler.CNIP != nil {
		handler.CNIP = newHandler.CNIP
	}
	if newHandler.HostsReaders != nil {
		handler.HostsReaders = newHandler.HostsReaders
	}
	if newHandler.GroupMap != nil {
		handler.GroupMap = newHandler.GroupMap
	}
}