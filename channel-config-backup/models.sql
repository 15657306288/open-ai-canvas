--
-- PostgreSQL database dump
--

\restrict Ylr9Ji9r1MjoQMGDtgd8rYrBVmtbqFBevg3PtTcf9PWb5hgKN98c4Aa98iuirsV

-- Dumped from database version 17.11
-- Dumped by pg_dump version 17.11

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Data for Name: channel_models; Type: TABLE DATA; Schema: public; Owner: open_ai_canvas
--

INSERT INTO public.channel_models VALUES ('MODEL_AGNES_TEXT', 'CHANNEL_000008', 'agnes-2.5-flash', 'agnes-2.5-flash', 'Agnes 2.5 Flash', NULL, 'text', 'chat-completion', 'token', NULL, 1000.000000, 1000.000000, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:12:28.006381+00', '2026-09-02 05:12:28.006381+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_AGNES_IMAGE', 'CHANNEL_000008', 'agnes-image-2.5-flash', 'agnes-image-2.5-flash', 'Agnes Image 2.5 Flash', NULL, 'image', 'agnes-image', 'fixed_request', 10000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:12:28.0099+00', '2026-09-02 05:12:28.0099+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_AGNES_VIDEO', 'CHANNEL_000008', 'agnes-video-2.5', 'agnes-video-2.5', 'Agnes Video 2.5', NULL, 'video', 'agnes-video', 'per_second', 180000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:12:28.011619+00', '2026-09-02 05:12:28.011619+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_AGNES_VIDEO_FLASH', 'CHANNEL_000008', 'agnes-video-2.5-flash', 'agnes-video-2.5-flash', 'Agnes Video 2.5 Flash', NULL, 'video', 'agnes-video', 'per_second', 10000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:12:28.012658+00', '2026-09-02 05:12:28.012658+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_GRSAI_GPT_IMAGE_2', 'CHANNEL_000006', 'gpt-image-2', 'gpt-image-2', 'GPT Image 2', NULL, 'image', 'grsai-image', 'fixed_request', 60000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:34:06.468495+00', '2026-09-02 05:34:06.468495+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_GRSAI_GPT_IMAGE_2_VIP', 'CHANNEL_000006', 'gpt-image-2-vip', 'gpt-image-2-vip', 'GPT Image 2 VIP', NULL, 'image', 'grsai-image', 'fixed_request', 200000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:34:06.470592+00', '2026-09-02 05:34:06.470592+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_GRSAI_NANO_BANANA_PRO', 'CHANNEL_000006', 'nano-banana-pro', 'nano-banana-pro', 'Nano Banana Pro', NULL, 'image', 'grsai-image', 'fixed_request', 180000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:34:06.471198+00', '2026-09-02 05:34:06.471198+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_GRSAI_NANO_BANANA_FAST', 'CHANNEL_000006', 'nano-banana-fast', 'nano-banana-fast', 'Nano Banana Fast', NULL, 'image', 'grsai-image', 'fixed_request', 50000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:34:06.471798+00', '2026-09-02 05:34:06.471798+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_GRSAI_NANO_BANANA_2', 'CHANNEL_000006', 'nano-banana-2', 'nano-banana-2', 'Nano Banana 2', NULL, 'image', 'grsai-image', 'fixed_request', 120000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:34:06.472289+00', '2026-09-02 05:34:06.472289+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GPT56_TERRA', 'CHANNEL_000007', 'gpt-5.6-terra', 'gpt-5.6-terra', 'GPT 5.6 Terra', NULL, 'text', 'chat-completion', 'token', NULL, 0.039977, 0.239864, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.738895+00', '2026-09-02 05:35:48.738895+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GPT56_SOL', 'CHANNEL_000007', 'gpt-5.6-sol', 'gpt-5.6-sol', 'GPT 5.6 Sol', NULL, 'text', 'chat-completion', 'token', NULL, 0.069259, 0.415555, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.740544+00', '2026-09-02 05:35:48.740544+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_DS_V4_FLASH', 'CHANNEL_000007', 'DeepSeek-V4-Flash', 'DeepSeek-V4-Flash-0731', 'DeepSeek V4 Flash', NULL, 'text', 'chat-completion', 'token', NULL, 0.166793, 0.500379, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.740993+00', '2026-09-02 05:35:48.740993+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GPT56_LUNA', 'CHANNEL_000007', 'gpt-5.6-luna', 'gpt-5.6-luna', 'GPT 5.6 Luna', NULL, 'text', 'chat-completion', 'token', NULL, 0.065522, 0.393070, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.741396+00', '2026-09-02 05:35:48.741396+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GLM_53', 'CHANNEL_000007', 'glm-5.3', 'glm-5.3', 'GLM 5.3', NULL, 'text', 'chat-completion', 'token', NULL, 3.997343, 12.563428, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.741753+00', '2026-09-02 05:35:48.741753+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_DS_V4_PRO', 'CHANNEL_000007', 'deepseek-v4-pro', 'deepseek-v4-pro-0813', 'DeepSeek V4 Pro', NULL, 'text', 'chat-completion', 'token', NULL, 1.345549, 4.036647, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.742053+00', '2026-09-02 05:35:48.742053+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_CLAUDE_OPUS_5', 'CHANNEL_000007', 'claude-opus-5', 'claude-opus-5', 'Claude Opus 5', NULL, 'text', 'chat-completion', 'token', NULL, 1.863475, 9.317376, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.742331+00', '2026-09-02 05:35:48.742331+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_CLAUDE_SONNET_5', 'CHANNEL_000007', 'claude-sonnet-5', 'claude-sonnet-5', 'Claude Sonnet 5', NULL, 'text', 'chat-completion', 'token', NULL, 0.816421, 4.082103, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.742665+00', '2026-09-02 05:35:48.742665+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_CLAUDE_FABLE_5', 'CHANNEL_000007', 'claude-fable-5', 'claude-fable-5', 'Claude Fable 5', NULL, 'text', 'chat-completion', 'token', NULL, 3.275640, 16.378200, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.742954+00', '2026-09-02 05:35:48.742954+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GROK_46', 'CHANNEL_000007', 'grok-4.6', 'grok-4.6', 'Grok 4.6', NULL, 'text', 'chat-completion', 'token', NULL, 0.026205, 0.078615, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.743555+00', '2026-09-02 05:35:48.743555+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_DS_V4_FLASH_VISION', 'CHANNEL_000007', 'deepseek-v4-flash-vision-exp', 'deepseek-v4-flash-vision-exp', 'DeepSeek V4 Flash Vision', NULL, 'text', 'chat-completion', 'token', NULL, 0.330873, 0.992618, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.743872+00', '2026-09-02 05:35:48.743872+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GLM_53_FLASH', 'CHANNEL_000007', 'glm-5.3-flash', 'glm-5.3-flash', 'GLM 5.3 Flash', NULL, 'text', 'chat-completion', 'token', NULL, 0.659981, 2.199936, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.744132+00', '2026-09-02 05:35:48.744132+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_GEMINI_37_FLASH', 'CHANNEL_000007', 'gemini-3.7-flash', 'gemini-3.7-flash', 'Gemini 3.7 Flash', NULL, 'text', 'chat-completion', 'token', NULL, 0.021314, 0.106568, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.744418+00', '2026-09-02 05:35:48.744418+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_KIMI_K3', 'CHANNEL_000007', 'kimi-k3', 'kimi-k3', 'Kimi K3', NULL, 'text', 'chat-completion', 'token', NULL, 3.684561, 18.422072, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.744702+00', '2026-09-02 05:35:48.744702+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_MINIMAX_M3', 'CHANNEL_000007', 'minimax-m3', 'minimax-m3', 'MiniMax M3', NULL, 'text', 'chat-completion', 'token', NULL, 0.066119, 0.264478, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.745004+00', '2026-09-02 05:35:48.745004+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_A6_QWEN38_MAX', 'CHANNEL_000007', 'qwen3.8-max', 'qwen3.8-max', 'Qwen 3.8 Max', NULL, 'text', 'chat-completion', 'token', NULL, 3.520585, 10.561755, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:35:48.745214+00', '2026-09-02 05:35:48.745214+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_PH_GPT_IMAGE_2K', 'CHANNEL_000005', 'ph-gpt-image-2k', 'ph-gpt-image-2k', 'PH GPT Image 2K', NULL, 'image', 'hongniao-image', 'fixed_request', 50000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:41:18.63205+00', '2026-09-02 05:41:18.63205+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_PH_GPT_IMAGE_4K', 'CHANNEL_000005', 'ph-gpt-image-4k', 'ph-gpt-image-4k', 'PH GPT Image 4K', NULL, 'image', 'hongniao-image', 'fixed_request', 100000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:41:18.633261+00', '2026-09-02 05:41:18.633261+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_SEEDANCE_20', 'CHANNEL_000005', 'seedance2.0', 'seedance2.0', 'Seedance 2.0', NULL, 'video', 'hongniao-video', 'per_second', 50000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:41:18.588196+00', '2026-09-02 05:43:58.244269+00', '2026-09-02 05:43:58.244269+00');
INSERT INTO public.channel_models VALUES ('MODEL_HN_SEEDANCE_25', 'CHANNEL_000005', 'seedance2.5', 'seedance2.5', 'Seedance 2.5', NULL, 'video', 'hongniao-video', 'per_second', 100000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:41:18.589919+00', '2026-09-02 05:43:58.244269+00', '2026-09-02 05:43:58.244269+00');
INSERT INTO public.channel_models VALUES ('MODEL_HN_WAN_30', 'CHANNEL_000005', 'wan3.0', 'wan3.0', 'Wan 3.0', NULL, 'video', 'hongniao-video', 'per_second', 80000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:41:18.590396+00', '2026-09-02 05:43:58.244269+00', '2026-09-02 05:43:58.244269+00');
INSERT INTO public.channel_models VALUES ('MODEL_METASO_MINIMAX_H3', 'CHANNEL_000009', 'MiniMax-H3', 'MiniMax-H3', 'MiniMax H3', NULL, 'video', 'minimax-video', 'per_second', 90000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:55:15.815745+00', '2026-09-02 05:55:15.815745+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_WAN3_720P', 'CHANNEL_000005', 'wan3-720p', 'wan3-720p', 'Wan 3.0 720P', NULL, 'video', 'hongniao-video', 'per_second', 200000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:43:58.296502+00', '2026-09-02 06:21:22.999162+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_HNSD2_FAST', 'CHANNEL_000005', 'hnsd2-fast-933-720p', 'hnsd2-fast-933-720p', 'HNSD2 Fast 720P', NULL, 'video', 'hongniao-video', 'fixed_request', 2800000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:43:58.299345+00', '2026-09-02 06:21:23.003828+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_SD20_9TU', 'CHANNEL_000005', 'seedance2.0-9tu-manxue', 'seedance2.0-9tu-manxue', 'Seedance 2.0 9图慢学', NULL, 'video', 'hongniao-video', 'fixed_request', 2800000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:43:58.299048+00', '2026-09-02 06:21:23.00418+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_SD25_720P', 'CHANNEL_000005', 'video-sd25-720p', 'video-sd25-720p', 'Seedance 2.5 720P', NULL, 'video', 'hongniao-video', 'per_second', 100000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:43:58.297854+00', '2026-09-02 06:25:20.564297+00', '2026-09-02 06:25:20.564297+00');
INSERT INTO public.channel_models VALUES ('MODEL_HN_SD25_480P', 'CHANNEL_000005', 'video-sd25-480p', 'video-sd25-480p', 'Seedance 2.5 480P', NULL, 'video', 'hongniao-video', 'per_second', 80000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:43:58.29828+00', '2026-09-02 06:25:20.564297+00', '2026-09-02 06:25:20.564297+00');
INSERT INTO public.channel_models VALUES ('MODEL_HN_SD25_C8_PRO', 'CHANNEL_000005', 'sd2.5-c8-pro-chao-720p', 'sd2.5-c8-pro-chao-720p', 'sd2.5-c8-pro-chao-720p', NULL, 'video', 'hongniao-video', 'per_second', 350000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 06:25:20.621146+00', '2026-09-02 06:25:20.621146+00', NULL);
INSERT INTO public.channel_models VALUES ('MODEL_HN_DS_25', 'CHANNEL_000005', 'video-ds-2.5', 'video-ds-2.5', 'video-ds-2.5', NULL, 'video', 'hongniao-video', 'per_second', 530000.000000, NULL, NULL, NULL, true, true, NULL, NULL, NULL, '2026-09-02 05:43:58.298726+00', '2026-09-02 06:25:20.665069+00', NULL);


--
-- PostgreSQL database dump complete
--

\unrestrict Ylr9Ji9r1MjoQMGDtgd8rYrBVmtbqFBevg3PtTcf9PWb5hgKN98c4Aa98iuirsV

