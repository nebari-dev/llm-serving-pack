import lockup from '@/assets/brand/Nebari-Logo-Horizontal-Lockup.svg'
import lockupWhite from '@/assets/brand/Nebari-Logo-Horizontal-Lockup-White-text.svg'
import { useTheme } from '@/hooks/theme-provider'

// The two brand lockups are framed differently: the dark-text file ships with
// a wide transparent margin (viewBox 1189.2 × 642.9, artwork at x 269–879,
// y 241–395), while the white-text file is already cropped to its artwork
// (viewBox 610.45 × 153.43). The assets are CC BY-NC-ND, so rather than edit
// them we crop in CSS using each file's own geometry and size by the artwork.
const assets = {
  light: { src: lockup, vb: { w: 1189.2, h: 642.9 }, art: { x: 269, y: 241, w: 610, h: 154 } },
  dark: { src: lockupWhite, vb: { w: 610.45, h: 153.43 }, art: { x: 0, y: 0, w: 610.45, h: 153.43 } },
}

export function NebariLogo({ height = 32 }: { height?: number }) {
  const { isDarkMode } = useTheme()
  const { src, vb, art } = assets[isDarkMode ? 'dark' : 'light']
  const scale = height / art.h
  return (
    <span className="relative block shrink-0 overflow-hidden" style={{ width: art.w * scale, height }}>
      <img
        src={src}
        alt="Nebari"
        className="absolute max-w-none"
        style={{ width: vb.w * scale, height: vb.h * scale, left: -art.x * scale, top: -art.y * scale }}
      />
    </span>
  )
}
