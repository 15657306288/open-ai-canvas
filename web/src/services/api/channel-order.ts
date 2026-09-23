import { http } from "./request";
export type ChannelOrderItem = { id: string; name: string; enabled: boolean };
const pathFor = (channelId?: string) => (channelId ? `/admin/channels/${encodeURIComponent(channelId)}/models/order` : "/admin/channels/order");
export const getChannelOrder = (channelId?: string) => http.get<{ items: ChannelOrderItem[] }>(pathFor(channelId));
export const saveChannelOrder = (channelId: string | undefined, ids: string[], expectedIds: string[]) => http.put<{ saved: boolean }>(pathFor(channelId), { ids, expectedIds });
