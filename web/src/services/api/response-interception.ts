import { http } from "@/services/api/request";

export type ResponseInterceptionRule = {
    contains: string;
    replace: string;
};

export type ResponseInterceptionSetting = {
    enabled: boolean;
    rules: ResponseInterceptionRule[];
};

export function getAdminResponseInterceptionSetting() {
    return http.get<{ setting: ResponseInterceptionSetting }>("/admin/settings/response-interception");
}

export function updateAdminResponseInterceptionSetting(setting: ResponseInterceptionSetting) {
    return http.patch<{ setting: ResponseInterceptionSetting }>("/admin/settings/response-interception", setting);
}
