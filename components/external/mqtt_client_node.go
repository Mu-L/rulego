/*
 * Copyright 2023 The RuleGo Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package external

import (
	"context"
	"sync"
	"time"

	"github.com/rulego/rulego/utils/mqtt"

	"github.com/rulego/rulego/api/types"
	"github.com/rulego/rulego/components/base"
	"github.com/rulego/rulego/utils/maps"
	"github.com/rulego/rulego/utils/str"
)

// 规则链节点配置示例：
//
//	{
//	       "id": "s3",
//	       "type": "mqttClient",
//	       "name": "mqtt推送数据",
//	       "debugMode": false,
//	       "configuration": {
//	         "Server": "127.0.0.1:1883",
//	         "Topic": "/device/msg"
//	       }
//	     }

func init() {
	Registry.Add(&MqttClientNode{})
}

type MqttClientNodeConfiguration struct {
	Server   string
	Username string
	Password string
	// Topic 发布主题 可以使用 ${metadata.key} 读取元数据中的变量或者使用 ${msg.key} 读取消息负荷中的变量进行替换
	Topic string
	//MaxReconnectInterval 重连间隔 单位秒
	MaxReconnectInterval int
	QOS                  uint8
	CleanSession         bool
	ClientID             string
	CAFile               string
	CertFile             string
	CertKeyFile          string
}

func (x *MqttClientNodeConfiguration) ToMqttConfig() mqtt.Config {
	if x.MaxReconnectInterval < 0 {
		x.MaxReconnectInterval = 60
	}
	return mqtt.Config{
		Server:               x.Server,
		Username:             x.Username,
		Password:             x.Password,
		QOS:                  x.QOS,
		MaxReconnectInterval: time.Duration(x.MaxReconnectInterval) * time.Second,
		CleanSession:         x.CleanSession,
		ClientID:             x.ClientID,
		CAFile:               x.CAFile,
		CertFile:             x.CertFile,
		CertKeyFile:          x.CertKeyFile,
	}
}

type MqttClientNode struct {
	base.SharedNode[*mqtt.Client]
	//节点配置
	Config MqttClientNodeConfiguration
	//topic 模板
	topicTemplate str.Template
	client        *mqtt.Client
	clientMutex   sync.RWMutex // Add mutex for thread safety
}

// Type 组件类型
func (x *MqttClientNode) Type() string {
	return "mqttClient"
}

func (x *MqttClientNode) New() types.Node {
	return &MqttClientNode{Config: MqttClientNodeConfiguration{
		Topic:                "/device/msg",
		Server:               "127.0.0.1:1883",
		QOS:                  0,
		MaxReconnectInterval: 60,
	}}
}

// Init 初始化
func (x *MqttClientNode) Init(ruleConfig types.Config, configuration types.Configuration) error {
	err := maps.Map2Struct(configuration, &x.Config)
	if err == nil {
		_ = x.SharedNode.Init(ruleConfig, x.Type(), x.Config.Server, ruleConfig.NodeClientInitNow, func() (*mqtt.Client, error) {
			return x.initClient()
		})
		x.topicTemplate = str.NewTemplate(x.Config.Topic)
	}
	return err
}

// OnMsg 处理消息
func (x *MqttClientNode) OnMsg(ctx types.RuleContext, msg types.RuleMsg) {
	topic := x.topicTemplate.ExecuteFn(func() map[string]any {
		return base.NodeUtils.GetEvnAndMetadata(ctx, msg)
	})
	if client, err := x.SharedNode.Get(); err != nil {
		ctx.TellFailure(msg, err)
	} else {
		if err := client.Publish(topic, x.Config.QOS, []byte(msg.GetData())); err != nil {
			ctx.TellFailure(msg, err)
		} else {
			ctx.TellSuccess(msg)
		}
	}
}

// Destroy 销毁
func (x *MqttClientNode) Destroy() {
	x.clientMutex.RLock()
	client := x.client
	x.clientMutex.RUnlock()

	if client != nil {
		_ = client.Close()
	}
}

// initClient 初始化客户端
func (x *MqttClientNode) initClient() (*mqtt.Client, error) {
	x.Locker.Lock()
	defer x.Locker.Unlock()

	x.clientMutex.RLock()
	if x.client != nil {
		existingClient := x.client
		x.clientMutex.RUnlock()
		return existingClient, nil
	}
	x.clientMutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.TODO(), 4*time.Second)
	defer cancel()

	client, err := mqtt.NewClient(ctx, x.Config.ToMqttConfig())
	if err == nil {
		x.clientMutex.Lock()
		x.client = client
		x.clientMutex.Unlock()
	}
	return client, err
}
