tailwind.config = {
	theme: {
		extend: {
			colors: {
				ink:      '#0b0c10',
				surface:  '#12141a',
				surface2: '#1c1f27',
				line:     'rgba(255,255,255,0.16)',
				lamp:     '#f0b429',
				lampsoft: '#f7c948',
				teal:     '#46c2b0',
				coral:    '#f26d6d',
				cream:    '#ece9e2',
				muted:    '#8f8d86',
			},
			fontFamily: {
				display: ['Schibsted Grotesk', 'Manrope', 'sans-serif'],
				displayheavy: ['Archivo Black', 'Schibsted Grotesk', 'Manrope', 'sans-serif'],
				sans: ['Manrope', 'sans-serif'],
			},
			fontWeight: {
				500: '500',
				600: '600',
				700: '700',
				800: '800',
				900: '900',
			},
			boxShadow: {
				hard: '4px 4px 0 0 #000',
				'hard-lamp': '4px 4px 0 0 rgba(240,180,41,0.45)',
				'hard-line': '4px 4px 0 0 rgba(255,255,255,0.16)',
			},
		}
	}
};
