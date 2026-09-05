import * as echarts from 'echarts/core';
import { LineChart, ScatterChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, LegendComponent, MarkAreaComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { createIcons, LayoutDashboard, Radio, Settings, MessageSquare, Info, Terminal, LogOut, Menu, RotateCw, Moon, Sun, Maximize, Activity, Wifi, Thermometer, ChevronRight, Cpu, MemoryStick } from 'lucide';

echarts.use([LineChart, ScatterChart, GridComponent, TooltipComponent, LegendComponent, MarkAreaComponent, CanvasRenderer]);
window.SimpleAdminCharts = echarts;
window.SimpleAdminIcons = () => createIcons({ icons: { LayoutDashboard, Radio, Settings, MessageSquare, Info, Terminal, LogOut, Menu, RotateCw, Moon, Sun, Maximize, Activity, Wifi, Thermometer, ChevronRight, Cpu, MemoryStick } });
