import { App, Button, Switch } from "antd";
import { RefreshCw } from "lucide-react";
import { useState } from "react";

import { SettingsRow } from "@/components/ui/product/settings-row";
import { refreshSystemChannels } from "@/lib/user-session";
import { isOptionalChannelEnabled, optionalChannelsForConfig, useConfigStore, type ModelChannel } from "@/stores/use-config-store";

/**
 * 可选渠道（豆包 / Dola 账号池）区块。
 * 目录里始终会有这些渠道；是否默认进入候选列表由目录的 defaultEnabled 决定，用户可随时自行关掉。
 * 启用后由平台账号池履约，不消耗平台额度。
 */
export function OptionalChannelsPane() {
    const { message } = App.useApp();
    const config = useConfigStore((state) => state.config);
    const setOptionalChannelEnabled = useConfigStore((state) => state.setOptionalChannelEnabled);
    const [refreshing, setRefreshing] = useState(false);
    const channels = optionalChannelsForConfig(config);

    if (!channels.length) return null;

    const refresh = async () => {
        setRefreshing(true);
        try {
            await refreshSystemChannels();
        } catch (error) {
            message.warning(error instanceof Error ? `账号池状态刷新失败：${error.message}` : "账号池状态刷新失败");
        } finally {
            setRefreshing(false);
        }
    };

    return (
        <section className="settings-preference-block" aria-labelledby="optional-channels-title">
            <div className="settings-preference-heading flex flex-wrap items-start gap-3">
                <div className="min-w-0 flex-1">
                    <h3 id="optional-channels-title">可选渠道</h3>
                    <p>用平台账号池生成，不消耗平台额度；高峰期可能排队，失败会自动换号。开启时模型会出现在模型选择列表里（标注「账号池 · 不扣费」），不想用时关掉即可。</p>
                </div>
                <Button size="small" icon={<RefreshCw className="size-4" />} loading={refreshing} onClick={() => void refresh()}>
                    刷新状态
                </Button>
            </div>
            <div className="settings-section">
                {channels.map((channel) => (
                    <SettingsRow
                        key={channel.id}
                        align="top"
                        label={channel.name}
                        description={channelDescription(channel)}
                        controlClassName="w-[200px]"
                        control={
                            <div className="flex flex-col items-end gap-2">
                                <Switch checked={isOptionalChannelEnabled(config, channel)} onChange={(checked) => setOptionalChannelEnabled(channel.id, checked)} aria-label={`启用${channel.name}`} />
                                <span className="text-right text-xs leading-4 text-foreground/55">{availabilityText(channel)}</span>
                            </div>
                        }
                    />
                ))}
            </div>
        </section>
    );
}

function channelDescription(channel: ModelChannel) {
    const labels = channel.models.map((model) => channel.modelCosts?.find((item) => item.model === model)?.displayName?.trim() || model).filter(Boolean);
    return labels.length ? `可用模型：${labels.join("、")}` : "该渠道当前没有可用模型";
}

function availabilityText(channel: ModelChannel) {
    const availability = channel.availability;
    if (!availability) return "账号池状态未知，刷新后查看";
    if (availability.state === "empty") return "暂无可用账号";
    if (availability.state === "error") return "账号池状态获取失败";
    return `可用账号 ${availability.readyAccounts}/${availability.totalAccounts}`;
}
